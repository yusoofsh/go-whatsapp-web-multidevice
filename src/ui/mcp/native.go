package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/google/uuid"
	mcpg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type nativeScopeKey struct{}
type nativeIdentityKey struct{}
type nativeIdentity struct{ principal, device string }
type nativeSession struct {
	identity   nativeIdentity
	touched    time.Time
	subscribed bool
}

// NativeHandler bypasses Fiber/fasthttp so SSE inherits real cancellation.
// Authentication is checked on EVERY request; session IDs are not credentials.
type NativeHandler struct {
	transport    *server.StreamableHTTPServer
	mcp          *server.MCPServer
	resolver     deviceResolver
	auth         func(*http.Request) (string, error)
	challenge    string
	origins      map[string]bool
	hosts        map[string]bool
	mu           sync.Mutex
	initializeMu sync.Mutex
	sessions     map[string]*nativeSession
	stop         func()
	closed       bool
}

func NewNativeHandler(deps Deps, resolver deviceResolver, auth func(*http.Request) (string, error), challenge string, origins []string) *NativeHandler {
	h := &NativeHandler{resolver: resolver, auth: auth, challenge: challenge, origins: make(map[string]bool), hosts: make(map[string]bool), sessions: make(map[string]*nativeSession)}
	for _, origin := range origins {
		if origin != "" && origin != "*" {
			h.origins[strings.TrimRight(origin, "/")] = true
			if u, err := url.Parse(origin); err == nil && u.Host != "" {
				h.hosts[u.Host] = true
			}
		}
	}
	hooks := &server.Hooks{}
	hooks.AddAfterSubscribe(func(ctx context.Context, _ any, r *mcpg.SubscribeRequest, _ *mcpg.EmptyResult) {
		h.subscription(ctx, r.Params.URI, true)
	})
	hooks.AddAfterUnsubscribe(func(ctx context.Context, _ any, r *mcpg.UnsubscribeRequest, _ *mcpg.EmptyResult) {
		h.subscription(ctx, r.Params.URI, false)
	})
	h.mcp = NewServer(deps, resolver, server.WithHooks(hooks), server.WithResourceCapabilities(deps.Data != nil, false))
	h.transport = server.NewStreamableHTTPServer(h.mcp,
		server.WithSessionIdManagerResolver(h), server.WithHeartbeatInterval(15*time.Second),
		server.WithSessionIdleTTL(10*time.Minute), server.WithDisableLocalhostProtection(true))
	// Host/Origin checks below replace the SDK's loopback-only guard, which would
	// otherwise reject a legitimate localhost reverse proxy with a public Host.
	if deps.Data != nil {
		h.stop = deps.Data.Listen(h.notify)
	}
	return h
}
func (h *NativeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	hostname := r.Host
	if host, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = host
	}
	loopback := hostname == "localhost"
	if ip := net.ParseIP(hostname); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback && !h.hosts[r.Host] {
		http.Error(w, "untrusted Host", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if !h.origins[origin] {
			http.Error(w, "untrusted Origin", 403)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version, WWW-Authenticate")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, X-Device-Id")
		w.WriteHeader(204)
		return
	}
	if h.auth == nil {
		http.Error(w, "MCP authentication is not configured", 503)
		return
	}
	principal, err := h.auth(r)
	if err != nil || principal == "" {
		if rateLimited, ok := err.(interface{ RetryAfter() time.Duration }); ok {
			retryAfter := int64((rateLimited.RetryAfter() + time.Second - 1) / time.Second)
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("WWW-Authenticate", h.challenge)
		http.Error(w, "Unauthorized", 401)
		return
	}
	identity := nativeIdentity{principal: principal}
	ctx := r.Context()
	if h.resolver != nil {
		d, _, err := h.resolver.ResolveDevice(strings.TrimSpace(r.Header.Get("X-Device-Id")))
		if err == nil && d != nil {
			identity.device = d.JID()
			if identity.device == "" {
				identity.device = "unpaired:" + d.ID()
			}
			ctx = whatsapp.ContextWithDevice(ctx, d)
		} else if r.Header.Get("X-Device-Id") != "" {
			http.Error(w, "unknown device", 400)
			return
		}
	}
	ctx = context.WithValue(ctx, nativeIdentityKey{}, identity)
	ctx = context.WithValue(ctx, nativeScopeKey{}, identity.device)
	r = r.WithContext(ctx)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "server shutting down", 503)
		return
	}
	now := time.Now()
	var expired []string
	for id, s := range h.sessions {
		if now.Sub(s.touched) > 10*time.Minute {
			delete(h.sessions, id)
			expired = append(expired, id)
		}
	}
	h.mu.Unlock()
	for _, id := range expired {
		h.mcp.UnregisterSession(context.Background(), id)
	}
	isInitialize := false
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpstore.MaxRequestBytes))
		if err != nil {
			http.Error(w, "request exceeds 15 MiB or could not be read", 413)
			return
		}
		var envelope struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				URI       string         `json:"uri"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			isInitialize = envelope.Method == "initialize"
			if envelope.Method == "initialize" {
				// Serialize admission, not ordinary tools, to enforce a hard session cap.
				h.initializeMu.Lock()
				defer h.initializeMu.Unlock()
				h.mu.Lock()
				full := len(h.sessions) >= 128
				h.mu.Unlock()
				if full {
					http.Error(w, "MCP session limit reached", 429)
					return
				}
			}
			// Tool-level device_id overrides the header default through
			// resolveDeviceContext. It does not mutate the authenticated
			// session identity used for GET/resources/subscriptions.
			if envelope.Method == "resources/subscribe" || envelope.Method == "resources/unsubscribe" {
				if envelope.Params.URI != mcpstore.EventResource || identity.device == "" || strings.HasPrefix(identity.device, "unpaired:") {
					h.rpcError(w, envelope.ID, "only whatsapp://events for a paired device supports subscriptions")
					return
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	// The SDK does not validate session IDs on GET. Enforce ownership here
	// for every non-initialize method, including GET, before opening a stream.
	if !isInitialize {
		terminated, err := h.ResolveSessionIdManager(r).Validate(r.Header.Get("Mcp-Session-Id"))
		if err != nil || terminated {
			http.Error(w, "invalid MCP session", http.StatusNotFound)
			return
		}
	}
	if r.Method == http.MethodGet {
		// Force periodic reauthentication, including after token revocation. A client
		// recovers missed notifications through the durable whatsapp_events cursor.
		streamCtx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		r = r.WithContext(streamCtx)
	}
	h.transport.ServeHTTP(w, r)
}
func (h *NativeHandler) rpcError(w http.ResponseWriter, id any, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": message}})
}
func (h *NativeHandler) subscription(ctx context.Context, uri string, value bool) {
	if uri != mcpstore.EventResource {
		return
	}
	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return
	}
	identity, ok := ctx.Value(nativeIdentityKey{}).(nativeIdentity)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if stored := h.sessions[session.SessionID()]; stored != nil && stored.identity == identity {
		stored.subscribed = value
	}
}
func (h *NativeHandler) notify(event mcpstore.Event) {
	h.mu.Lock()
	ids := make([]string, 0)
	for id, s := range h.sessions {
		if s.subscribed && s.identity.device == event.DeviceID {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		// SDK notification queues are bounded. The committed journal, not this signal,
		// is authoritative; a slow subscriber must not block WhatsApp message handling.
		_ = h.mcp.SendNotificationToSpecificClient(id, "notifications/resources/updated", map[string]any{"uri": mcpstore.EventResource})
	}
}
func (h *NativeHandler) Close(ctx context.Context) error {
	if h.stop != nil {
		h.stop()
	}
	h.mu.Lock()
	h.closed = true
	h.sessions = make(map[string]*nativeSession)
	h.mu.Unlock()
	return h.transport.Shutdown(ctx)
}
func (h *NativeHandler) ResolveSessionIdManager(r *http.Request) server.SessionIdManager {
	var identity *nativeIdentity
	if r != nil {
		if value, ok := r.Context().Value(nativeIdentityKey{}).(nativeIdentity); ok {
			identity = &value
		}
	}
	return &boundSessionManager{h: h, identity: identity}
}

type boundSessionManager struct {
	h        *NativeHandler
	identity *nativeIdentity
}

func (m *boundSessionManager) Generate() string {
	if m.identity == nil {
		return ""
	}
	id := uuid.NewString()
	m.h.mu.Lock()
	m.h.sessions[id] = &nativeSession{identity: *m.identity, touched: time.Now()}
	m.h.mu.Unlock()
	return id
}
func (m *boundSessionManager) Validate(id string) (bool, error) {
	m.h.mu.Lock()
	defer m.h.mu.Unlock()
	s := m.h.sessions[id]
	if s == nil || (m.identity != nil && s.identity != *m.identity) || time.Since(s.touched) > 10*time.Minute {
		return false, errors.New("invalid session")
	}
	s.touched = time.Now()
	return false, nil
}
func (m *boundSessionManager) Terminate(id string) (bool, error) {
	m.h.mu.Lock()
	defer m.h.mu.Unlock()
	s := m.h.sessions[id]
	if s == nil || (m.identity != nil && s.identity != *m.identity) {
		return false, errors.New("invalid session")
	}
	delete(m.h.sessions, id)
	return false, nil
}
func PublicOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
