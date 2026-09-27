package cmd

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/basicauth"
	"github.com/sirupsen/logrus"
)

const (
	basicAuthFailureLimit  = 5
	basicAuthFailureWindow = time.Minute
	basicAuthBlockDuration = 5 * time.Minute
	basicAuthMaxClients    = 4096
	basicAuthChallenge     = `Basic realm="Restricted", charset="UTF-8"`
)

type basicAuthFailureState struct {
	windowStart  time.Time
	failures     int
	blockedUntil time.Time
}

type basicAuthFailureResult struct {
	count        int
	blocked      bool
	newlyBlocked bool
	retryAfter   time.Duration
}

// basicAuthClientIP normalizes an address to a stable IP identity. A TCP
// source port is ephemeral and must not be part of an authentication budget:
// otherwise every reconnect would create a fresh bucket.
func basicAuthClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	return addr.Unmap().String()
}

// trustedProxyIP reports whether an address is explicitly configured as a
// trusted reverse proxy. X-Forwarded-For is ignored unless the immediate peer
// is trusted, so a direct client cannot select its own rate-limit identity.
func trustedProxyIP(raw string, proxies []string) bool {
	addr, err := netip.ParseAddr(basicAuthClientIP(raw))
	if err != nil {
		return false
	}
	for _, rawProxy := range proxies {
		rawProxy = strings.TrimSpace(rawProxy)
		if rawProxy == "" {
			continue
		}
		if strings.Contains(rawProxy, "/") {
			_, network, err := net.ParseCIDR(rawProxy)
			if err == nil && network.Contains(net.IP(addr.AsSlice())) {
				return true
			}
			continue
		}
		proxyIP := basicAuthClientIP(rawProxy)
		if proxyIP != "" && proxyIP == addr.String() {
			return true
		}
	}
	return false
}

// basicAuthForwardedClientIP applies the same right-to-left trusted-proxy
// rule used by Fiber for the native net/http MCP gateway. It never trusts an
// X-Forwarded-For value from an untrusted immediate peer.
func basicAuthForwardedClientIP(r *http.Request, trustedProxies []string) string {
	if r == nil {
		return ""
	}
	remoteIP := basicAuthClientIP(r.RemoteAddr)
	if remoteIP == "" || !trustedProxyIP(remoteIP, trustedProxies) {
		return remoteIP
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		candidate := basicAuthClientIP(forwarded[i])
		if candidate == "" {
			continue
		}
		if trustedProxyIP(candidate, trustedProxies) {
			continue
		}
		return candidate
	}
	return remoteIP
}

// basicAuthFailureLimiter is deliberately local to a server process. It only
// tracks failed Basic credentials; Bearer requests and successful credentials
// never consume the budget. The bounded map prevents an attacker from turning
// the limiter itself into an unbounded memory store.
type basicAuthFailureLimiter struct {
	mu         sync.Mutex
	entries    map[string]basicAuthFailureState
	maxClients int
	limit      int
	window     time.Duration
	block      time.Duration
	now        func() time.Time
}

func newBasicAuthFailureLimiter() *basicAuthFailureLimiter {
	return &basicAuthFailureLimiter{
		entries:    make(map[string]basicAuthFailureState),
		maxClients: basicAuthMaxClients,
		limit:      basicAuthFailureLimit,
		window:     basicAuthFailureWindow,
		block:      basicAuthBlockDuration,
		now:        time.Now,
	}
}

func (l *basicAuthFailureLimiter) recordFailure(key string) basicAuthFailureResult {
	if key == "" {
		key = "unknown"
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	state, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= l.maxClients {
			l.evictOldestLocked()
		}
		state = basicAuthFailureState{windowStart: now}
	}

	if !state.blockedUntil.IsZero() {
		if now.Before(state.blockedUntil) {
			return basicAuthFailureResult{
				count:      state.failures,
				blocked:    true,
				retryAfter: state.blockedUntil.Sub(now),
			}
		}
		state = basicAuthFailureState{windowStart: now}
	}
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= l.window {
		state = basicAuthFailureState{windowStart: now}
	}

	state.failures++
	result := basicAuthFailureResult{count: state.failures}
	if state.failures >= l.limit {
		state.blockedUntil = now.Add(l.block)
		result.blocked = true
		result.newlyBlocked = true
		result.retryAfter = l.block
	}
	l.entries[key] = state
	return result
}

// blocked returns the current cooldown without changing the failure budget.
// Callers must invoke this before credential verification so a blocked Basic
// request cannot continue to consume verifier work.
func (l *basicAuthFailureLimiter) blocked(key string) basicAuthFailureResult {
	if key == "" {
		key = "unknown"
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	state, ok := l.entries[key]
	if !ok || state.blockedUntil.IsZero() || !now.Before(state.blockedUntil) {
		return basicAuthFailureResult{}
	}
	return basicAuthFailureResult{
		count:      state.failures,
		blocked:    true,
		retryAfter: state.blockedUntil.Sub(now),
	}
}

func (l *basicAuthFailureLimiter) reset(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (l *basicAuthFailureLimiter) pruneLocked(now time.Time) {
	for key, state := range l.entries {
		if !state.blockedUntil.IsZero() {
			if !now.Before(state.blockedUntil) {
				delete(l.entries, key)
			}
			continue
		}
		if !state.windowStart.IsZero() && now.Sub(state.windowStart) >= l.window {
			delete(l.entries, key)
		}
	}
}

func (l *basicAuthFailureLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, state := range l.entries {
		if oldestKey == "" || state.windowStart.Before(oldest) {
			oldestKey = key
			oldest = state.windowStart
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func basicAuthClientKey(c fiber.Ctx) string {
	if ip := basicAuthClientIP(c.IP()); ip != "" {
		return ip
	}
	if addr := c.RequestCtx().RemoteAddr(); addr != nil {
		if ip := basicAuthClientIP(addr.String()); ip != "" {
			return ip
		}
	}
	return "unknown"
}

func basicAuthHTTPClientKey(r *http.Request, trustedProxies []string) string {
	if ip := basicAuthForwardedClientIP(r, trustedProxies); ip != "" {
		return ip
	}
	return "unknown"
}

func logBasicAuthThrottle(result basicAuthFailureResult) {
	if result.newlyBlocked {
		// Do not log usernames, passwords, Authorization headers, paths, or
		// client addresses. The event is still useful for detection while
		// avoiding credential and personal-data disclosure.
		logrus.Warn("[AUTH] Basic authentication failures throttled; credential and client details omitted")
	}
}

func basicAuthRateLimitedResponse(c fiber.Ctx, retryAfter time.Duration) error {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Set(fiber.HeaderRetryAfter, strconv.FormatInt(seconds, 10))
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(fiber.HeaderVary, fiber.HeaderAuthorization)
	return c.SendStatus(fiber.StatusTooManyRequests)
}

type basicAuthRateLimitError struct {
	retryAfter time.Duration
}

func (e basicAuthRateLimitError) Error() string {
	return "basic authentication temporarily throttled"
}

func (e basicAuthRateLimitError) RetryAfter() time.Duration {
	return e.retryAfter
}

func verifyBasicCredential(accounts map[string]string, username, password string) bool {
	expectedPassword, ok := accounts[username]
	if !ok {
		// Keep the comparison work independent of whether the username exists.
		expectedPassword = "gowa-basic-auth-dummy"
	}
	passwordHash := sha256Bytes(password)
	expectedHash := sha256Bytes(expectedPassword)
	return ok && subtle.ConstantTimeCompare(passwordHash[:], expectedHash[:]) == 1
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

// parseBasicAuthorization is intentionally only a classification helper for
// the limiter. Fiber's basicauth middleware remains the canonical parser for
// the REST surface; this helper must never be used to grant access by itself.
func parseBasicAuthorization(header string) (scheme, username, password string, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", "", false
	}
	parts := strings.SplitN(header, " ", 2)
	scheme = strings.ToLower(strings.TrimSpace(parts[0]))
	if scheme != "basic" {
		return scheme, "", "", false
	}
	if len(parts) != 2 {
		return scheme, "", "", false
	}
	encoded := strings.TrimSpace(parts[1])
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return scheme, "", "", false
	}
	username, password, ok = strings.Cut(string(decoded), ":")
	return scheme, username, password, ok
}

// newBasicAuthMiddleware retains Fiber's standards-compliant Basic parser and
// challenge while adding a bounded failure budget. The cooldown is checked
// before credential verification, so repeated guesses cannot keep invoking the
// verifier. A valid administrator can recover after the cooldown, or through
// an already-authenticated Bearer/MCP stream or an administrative side path.
func newBasicAuthMiddleware(accounts map[string]string) fiber.Handler {
	limiter := newBasicAuthFailureLimiter()
	auth := basicauth.New(basicauth.Config{
		Authorizer: func(username, password string, c fiber.Ctx) bool {
			key := basicAuthClientKey(c)
			if verifyBasicCredential(accounts, username, password) {
				limiter.reset(key)
				return true
			}

			result := limiter.recordFailure(key)
			logBasicAuthThrottle(result)
			c.Locals("gowa.basic-auth-failure", result)
			return false
		},
		Unauthorized: func(c fiber.Ctx) error {
			if result, ok := c.Locals("gowa.basic-auth-failure").(basicAuthFailureResult); ok && result.blocked {
				return basicAuthRateLimitedResponse(c, result.retryAfter)
			}
			c.Set(fiber.HeaderWWWAuthenticate, basicAuthChallenge)
			c.Set(fiber.HeaderCacheControl, "no-store")
			c.Set(fiber.HeaderVary, fiber.HeaderAuthorization)
			return c.SendStatus(fiber.StatusUnauthorized)
		},
	})

	return func(c fiber.Ctx) error {
		header := c.Get(fiber.HeaderAuthorization)
		scheme, _, _, parsed := parseBasicAuthorization(header)
		if scheme == "basic" {
			key := basicAuthClientKey(c)
			if result := limiter.blocked(key); result.blocked {
				return basicAuthRateLimitedResponse(c, result.retryAfter)
			}
			// Count malformed Basic attempts too, while leaving Bearer, missing,
			// and other schemes completely outside this limiter.
			if !parsed {
				result := limiter.recordFailure(key)
				logBasicAuthThrottle(result)
				if result.blocked {
					return basicAuthRateLimitedResponse(c, result.retryAfter)
				}
			}
		}
		return auth(c)
	}
}

// wrapMCPBasicAuthLimiter applies the same Basic-only failure policy around
// OAuth MCP middleware. Bearer requests, including long-lived MCP streams,
// never touch the Basic failure budget.
func wrapMCPBasicAuthLimiter(next fiber.Handler, validate func(string, string) bool, limiter *basicAuthFailureLimiter) fiber.Handler {
	return func(c fiber.Ctx) error {
		scheme, username, password, parsed := parseBasicAuthorization(c.Get(fiber.HeaderAuthorization))
		if scheme != "basic" {
			return next(c)
		}
		key := basicAuthClientKey(c)
		if result := limiter.blocked(key); result.blocked {
			return basicAuthRateLimitedResponse(c, result.retryAfter)
		}
		if parsed && validate(username, password) {
			limiter.reset(key)
			return next(c)
		}
		result := limiter.recordFailure(key)
		logBasicAuthThrottle(result)
		if result.blocked {
			return basicAuthRateLimitedResponse(c, result.retryAfter)
		}
		return next(c)
	}
}

func wrapOAuthAuthorizeBasicAuthLimiter(next fiber.Handler, validate func(string, string) bool, limiter *basicAuthFailureLimiter) fiber.Handler {
	return func(c fiber.Ctx) error {
		if c.Method() != fiber.MethodPost {
			return next(c)
		}
		form, err := url.ParseQuery(string(c.Body()))
		if err != nil {
			return next(c)
		}
		username, password := form.Get("username"), form.Get("password")
		if username == "" && password == "" {
			return next(c)
		}
		key := basicAuthClientKey(c)
		if result := limiter.blocked(key); result.blocked {
			return basicAuthRateLimitedResponse(c, result.retryAfter)
		}
		if validate(username, password) {
			limiter.reset(key)
			return next(c)
		}
		result := limiter.recordFailure(key)
		logBasicAuthThrottle(result)
		if result.blocked {
			return basicAuthRateLimitedResponse(c, result.retryAfter)
		}
		return next(c)
	}
}

func authenticateNativeBasic(r *http.Request, validate func(string, string) bool, limiter *basicAuthFailureLimiter, trustedProxies []string) (username string, valid bool, rateLimited error) {
	scheme, username, password, parsed := parseBasicAuthorization(r.Header.Get(fiber.HeaderAuthorization))
	if scheme != "basic" {
		return "", false, nil
	}
	key := basicAuthHTTPClientKey(r, trustedProxies)
	if result := limiter.blocked(key); result.blocked {
		return "", false, basicAuthRateLimitError{retryAfter: result.retryAfter}
	}
	if parsed && validate(username, password) {
		limiter.reset(key)
		return username, true, nil
	}
	result := limiter.recordFailure(key)
	logBasicAuthThrottle(result)
	if result.blocked {
		return "", false, basicAuthRateLimitError{retryAfter: result.retryAfter}
	}
	return "", false, errors.New("invalid Basic credentials")
}
