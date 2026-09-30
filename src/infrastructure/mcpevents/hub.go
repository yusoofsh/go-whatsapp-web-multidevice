// Package mcpevents implements signed ChatGPT MCP Events over the existing device journal.
package mcpevents

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
)

type Journal interface {
	Events(context.Context, string, int64, int) (mcpstore.EventPage, error)
}
type Subscription struct {
	ID            string            `json:"id"`
	Owner         string            `json:"owner"`
	Device        string            `json:"device"`
	Name          string            `json:"name"`
	Arguments     map[string]string `json:"arguments"`
	URL           string            `json:"url"`
	Secret        string            `json:"secret"`
	OldSecret     string            `json:"old_secret,omitempty"`
	RotateUntil   int64             `json:"rotate_until,omitempty"`
	DeviceHeader  string            `json:"device_header"`
	AuthHeader    string            `json:"auth_header"`
	Expires       int64             `json:"expires"`
	VerifiedUntil int64             `json:"verified_until"`
	Cursor        int64             `json:"cursor"`
	Attempts      int               `json:"attempts"`
	Due           int64             `json:"due"`
}
type Params struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
	Delivery  struct {
		Mode   string `json:"mode"`
		URL    string `json:"url"`
		Secret string `json:"secret"`
	} `json:"delivery"`
	Cursor *string         `json:"cursor"`
	TTL    json.RawMessage `json:"ttlMs"`
}
type RPCError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    map[string]string `json:"data,omitempty"`
}

func (e *RPCError) Error() string  { return e.Message }
func invalid(message string) error { return &RPCError{Code: -32602, Message: message} }

type Post func(context.Context, string, map[string]string, []byte) (int, []byte, error)
type Hub struct {
	mu            sync.Mutex
	path          string
	cipher        cipher.AEAD
	subscriptions map[string]Subscription
	journal       Journal
	Post          Post
	Authorized    func(Subscription) bool
	now           func() time.Time
}

var kinds = map[string]string{"message.created": "message", "message.edited": "message.edited", "message.revoked": "message.revoked", "message.reaction": "message.reaction", "message.receipt": "message.receipt", "message.deleted": "message.deleted", "connection.connected": "connection.connected", "connection.disconnected": "connection.disconnected", "connection.logged_out": "connection.logged_out", "group.updated": "group.updated"}

func Catalog() []map[string]any {
	result := make([]map[string]any, 0, len(kinds))
	for _, name := range []string{"message.created", "message.edited", "message.revoked", "message.reaction", "message.receipt", "message.deleted", "connection.connected", "connection.disconnected", "connection.logged_out", "group.updated"} {
		result = append(result, map[string]any{"name": name, "description": "Device-scoped WhatsApp journal update. Reference-only signed webhook delivery; fetch full records with existing WhatsApp read tools.", "delivery": []string{"webhook"}, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]string{"type": "string"}, "chat_id": map[string]string{"type": "string"}}, "required": []string{"device_id"}, "additionalProperties": false}, "payloadSchema": map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]string{"type": "string"}, "chat_id": map[string]string{"type": "string"}, "message_id": map[string]string{"type": "string"}}, "required": []string{"device_id", "chat_id", "message_id"}, "additionalProperties": false}})
	}
	return result
}
func Open(path, keyPath string, journal Journal, authorized func(Subscription) bool) (*Hub, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		_ = f.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("event state key must be 32 bytes")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("event state key must be owner-only")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	h := &Hub{path: path, cipher: aead, journal: journal, Post: SafePost, Authorized: authorized, now: time.Now, subscriptions: map[string]Subscription{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) < aead.NonceSize() {
			return nil, errors.New("invalid event state")
		}
		opened, e := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("mcp-events-v1"))
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(opened, &h.subscriptions); e != nil {
			return nil, e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return h, nil
}
func (h *Hub) save() error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(h.subscriptions)
	if err != nil {
		return err
	}
	nonce := make([]byte, h.cipher.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	data := h.cipher.Seal(nonce, nonce, body, []byte("mcp-events-v1"))
	f, err := os.CreateTemp(filepath.Dir(h.path), "events-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), h.path)
}
func key(secret string) ([]byte, error) {
	if !strings.HasPrefix(secret, "whsec_") {
		return nil, invalid("invalid webhook signing secret")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(raw) < 24 || len(raw) > 64 {
		return nil, invalid("invalid webhook signing secret")
	}
	return raw, nil
}
func callback(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || len(value) > 2048 || strings.ContainsAny(value, "\\\r\n\t ") || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, invalid("callback must use public HTTPS on port 443")
	}
	return u, nil
}
func Headers(s Subscription, id string, body []byte, now time.Time) (map[string]string, error) {
	if len(body) > 262144 {
		return nil, invalid("event payload exceeds 256 KiB")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	secrets := []string{s.Secret}
	if s.OldSecret != "" && s.RotateUntil > now.UnixMilli() {
		secrets = append(secrets, s.OldSecret)
	}
	signatures := []string{}
	for _, secret := range secrets {
		k, err := key(secret)
		if err != nil {
			return nil, err
		}
		mac := hmac.New(sha256.New, k)
		_, _ = mac.Write([]byte(id + "." + ts + "."))
		_, _ = mac.Write(body)
		signatures = append(signatures, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	return map[string]string{"Content-Type": "application/json", "webhook-id": id, "webhook-timestamp": ts, "webhook-signature": strings.Join(signatures, " "), "X-MCP-Subscription-Id": s.ID}, nil
}
func (h *Hub) Handle(ctx context.Context, method string, raw json.RawMessage, owner, device, authorization, deviceHeader string) (any, error) {
	if owner == "" {
		return nil, &RPCError{Code: -32001, Message: "authenticated event owner required"}
	}
	if method == "events/list" {
		return map[string]any{"events": Catalog()}, nil
	}
	if method != "events/subscribe" && method != "events/unsubscribe" {
		return nil, &RPCError{Code: -32601, Message: "unknown event method"}
	}
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, invalid("invalid event arguments")
	}
	if _, ok := kinds[p.Name]; !ok {
		return nil, invalid("unknown event")
	}
	if p.Delivery.Mode != "webhook" {
		return nil, invalid("only webhook delivery is supported")
	}
	if _, err := callback(p.Delivery.URL); err != nil {
		return nil, err
	}
	if device == "" || strings.HasPrefix(device, "unpaired:") || p.Arguments["device_id"] != device {
		return nil, &RPCError{Code: -32001, Message: "device filter must match the authenticated paired device"}
	}
	for k, v := range p.Arguments {
		if (k != "device_id" && k != "chat_id") || v == "" || len(v) > 256 {
			return nil, invalid("invalid event filter")
		}
	}
	identity, _ := json.Marshal([]any{owner, p.Delivery.URL, p.Name, p.Arguments})
	sum := sha256.Sum256(identity)
	id := "sub_" + hex.EncodeToString(sum[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	if method == "events/unsubscribe" {
		delete(h.subscriptions, id)
		return map[string]any{}, h.save()
	}
	if _, err := key(p.Delivery.Secret); err != nil {
		return nil, err
	}
	ttl := int64(300000)
	if len(p.TTL) > 0 && string(p.TTL) != "null" {
		if err := json.Unmarshal(p.TTL, &ttl); err != nil || ttl <= 0 {
			return nil, invalid("invalid ttlMs")
		}
	}
	if ttl > 300000 {
		ttl = 300000
	}
	now := h.now()
	for key, sub := range h.subscriptions {
		if sub.Expires <= now.UnixMilli() {
			delete(h.subscriptions, key)
		}
	}
	old, exists := h.subscriptions[id]
	if exists && old.Expires <= now.UnixMilli() {
		exists = false
	}
	if !exists && len(h.subscriptions) >= 128 {
		return nil, &RPCError{Code: -32000, Message: "event subscription limit reached"}
	}
	cursor := old.Cursor
	requested := false
	if p.Cursor != nil {
		value, err := strconv.ParseInt(*p.Cursor, 10, 64)
		if err != nil || value < 0 {
			return nil, invalid("invalid replay cursor")
		}
		cursor = value
		requested = true
	}
	if !exists && !requested {
		page, err := h.journal.Events(ctx, device, 0, 1)
		if err != nil {
			return nil, err
		}
		cursor = page.PurgedThrough
		for page.HasMore {
			page, err = h.journal.Events(ctx, device, page.NextCursor, 500)
			if err != nil {
				return nil, err
			}
		}
		if page.NextCursor > cursor {
			cursor = page.NextCursor
		}
	}
	s := Subscription{ID: id, Owner: owner, Device: device, Name: p.Name, Arguments: p.Arguments, URL: p.Delivery.URL, Secret: p.Delivery.Secret, AuthHeader: authorization, DeviceHeader: deviceHeader, Expires: now.Add(time.Duration(ttl) * time.Millisecond).UnixMilli(), VerifiedUntil: now.Add(5 * time.Minute).UnixMilli(), Cursor: cursor}
	if !h.Authorized(s) {
		return nil, &RPCError{Code: -32001, Message: "event owner access revoked"}
	}
	if exists && old.Secret != s.Secret {
		s.OldSecret = old.Secret
		s.RotateUntil = now.Add(5 * time.Minute).UnixMilli()
	}
	if exists && old.Secret == s.Secret {
		s.OldSecret = old.OldSecret
		s.RotateUntil = old.RotateUntil
		s.VerifiedUntil = old.VerifiedUntil
	}
	if !exists || old.Secret != s.Secret || old.VerifiedUntil <= now.UnixMilli() {
		challengeBytes := make([]byte, 32)
		_, _ = rand.Read(challengeBytes)
		challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
		body, _ := json.Marshal(map[string]string{"type": "verification", "challenge": challenge})
		verificationID := "msg_verification_" + hex.EncodeToString(challengeBytes)
		headers, _ := Headers(s, verificationID, body, now)
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		status, response, err := h.Post(bounded, s.URL, headers, body)
		cancel()
		var echo struct {
			Challenge string `json:"challenge"`
		}
		if err != nil || status < 200 || status >= 300 || json.Unmarshal(response, &echo) != nil || subtle.ConstantTimeCompare([]byte(challenge), []byte(echo.Challenge)) != 1 {
			return nil, &RPCError{Code: -32015, Message: "callback verification failed", Data: map[string]string{"reason": "challenge_failed"}}
		}
	}
	page, err := h.journal.Events(ctx, device, cursor, 1)
	if err != nil {
		return nil, err
	}
	truncated := page.CursorExpired || (requested && cursor < page.PurgedThrough)
	if truncated {
		s.Cursor = page.PurgedThrough
	}
	h.subscriptions[id] = s
	if err = h.save(); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "refreshBefore": time.UnixMilli(s.Expires).UTC().Format(time.RFC3339Nano), "cursor": strconv.FormatInt(s.Cursor, 10), "truncated": truncated}, nil
}
func (h *Hub) Tick(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if len(h.subscriptions) == 0 {
		return nil
	}
	for id, s := range h.subscriptions {
		if s.Expires <= now.UnixMilli() || !h.Authorized(s) {
			delete(h.subscriptions, id)
			continue
		}
		if s.Due > now.UnixMilli() {
			continue
		}
		page, err := h.journal.Events(ctx, s.Device, s.Cursor, 20)
		if err != nil {
			return err
		}
		if page.CursorExpired {
			s.Cursor = page.PurgedThrough
		}
		for _, e := range page.Events {
			if !h.Authorized(s) {
				delete(h.subscriptions, id)
				break
			}
			if e.Type != kinds[s.Name] || (s.Arguments["chat_id"] != "" && s.Arguments["chat_id"] != e.ChatJID) {
				s.Cursor = e.ID
				continue
			}
			eventID := fmt.Sprintf("evt_%x_%d", sha256.Sum256([]byte(e.DeviceID)), e.ID)
			body, _ := json.Marshal(map[string]any{"eventId": eventID, "name": s.Name, "timestamp": time.Unix(e.Timestamp, 0).UTC().Format(time.RFC3339), "data": map[string]string{"device_id": e.DeviceID, "chat_id": e.ChatJID, "message_id": e.MessageID}, "cursor": strconv.FormatInt(e.ID, 10)})
			headers, err := Headers(s, eventID, body, now)
			if err != nil {
				return err
			}
			bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
			status, _, sendErr := h.Post(bounded, s.URL, headers, body)
			cancel()
			if status == 410 {
				delete(h.subscriptions, id)
				break
			}
			if sendErr != nil || status == 429 || status >= 500 {
				if s.Attempts < 7 {
					s.Due = now.Add(time.Second * time.Duration(1<<s.Attempts)).UnixMilli()
					s.Attempts++
					break
				}
			}
			s.Cursor = e.ID
			s.Attempts = 0
			s.Due = 0
		}
		if _, exists := h.subscriptions[id]; exists {
			h.subscriptions[id] = s
		}
	}
	return h.save()
}
func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}
func SafePost(ctx context.Context, value string, headers map[string]string, body []byte) (int, []byte, error) {
	u, err := callback(value)
	if err != nil {
		return 0, nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil || len(ips) == 0 {
		return 0, nil, errors.New("callback resolution failed")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return 0, nil, errors.New("callback destination is not public")
		}
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), "443"))
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 {
		return 0, nil, errors.New("callback response exceeds limit")
	}
	return response.StatusCode, data, nil
}
