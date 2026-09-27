package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicAuthFailureLimiterAllowsCorrectCredentialBeforeCooldown(t *testing.T) {
	app := fiber.New()
	app.Use(newBasicAuthMiddleware(map[string]string{"user": "secret"}))
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	for attempt := 1; attempt < basicAuthFailureLimit; attempt++ {
		resp := basicAuthTestRequest(t, app, "user", "wrong")
		assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode, "attempt %d", attempt)
	}

	// A correct credential before the cooldown clears the failure budget.
	resp := basicAuthTestRequest(t, app, "user", "secret")
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	resp = basicAuthTestRequest(t, app, "user", "wrong")
	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
}

func TestBasicAuthFailureLimiterBlocksCredentialVerificationDuringCooldown(t *testing.T) {
	app := fiber.New()
	app.Use(newBasicAuthMiddleware(map[string]string{"user": "secret"}))
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	for attempt := 1; attempt <= basicAuthFailureLimit; attempt++ {
		resp := basicAuthTestRequest(t, app, "user", "wrong")
		if attempt == basicAuthFailureLimit {
			assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
		} else {
			assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
		}
	}

	resp := basicAuthTestRequest(t, app, "user", "secret")
	assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get(fiber.HeaderRetryAfter))
	assert.Equal(t, "no-store", resp.Header.Get(fiber.HeaderCacheControl))
}

func TestBasicAuthClientKeyNormalizesReconnectPorts(t *testing.T) {
	first := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	first.RemoteAddr = "192.0.2.10:41001"
	second := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	second.RemoteAddr = "192.0.2.10:41002"
	assert.Equal(t, basicAuthHTTPClientKey(first, nil), basicAuthHTTPClientKey(second, nil))
}

func TestBasicAuthForwardedClientIPRequiresTrustedImmediatePeer(t *testing.T) {
	trusted := []string{"10.0.0.0/8"}

	fromTrustedProxy := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	fromTrustedProxy.RemoteAddr = "10.10.10.10:443"
	fromTrustedProxy.Header.Set("X-Forwarded-For", "198.51.100.10")
	assert.Equal(t, "198.51.100.10", basicAuthHTTPClientKey(fromTrustedProxy, trusted))

	spoofedByClient := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	spoofedByClient.RemoteAddr = "192.0.2.20:443"
	spoofedByClient.Header.Set("X-Forwarded-For", "198.51.100.10")
	assert.Equal(t, "192.0.2.20", basicAuthHTTPClientKey(spoofedByClient, trusted))

	proxyChain := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	proxyChain.RemoteAddr = "10.10.10.10:443"
	proxyChain.Header.Set("X-Forwarded-For", "198.51.100.10, 10.20.20.20")
	assert.Equal(t, "198.51.100.10", basicAuthHTTPClientKey(proxyChain, trusted))
}

func TestBasicAuthLimiterDoesNotMixClients(t *testing.T) {
	limiter := newBasicAuthFailureLimiter()
	for range basicAuthFailureLimit {
		limiter.recordFailure("192.0.2.30")
	}
	assert.True(t, limiter.blocked("192.0.2.30").blocked)
	assert.False(t, limiter.blocked("192.0.2.31").blocked)
}

func TestAuthenticateNativeBasicBlockedAttemptSkipsValidator(t *testing.T) {
	limiter := newBasicAuthFailureLimiter()
	for range basicAuthFailureLimit {
		limiter.recordFailure("192.0.2.40")
	}

	request := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	request.RemoteAddr = "192.0.2.40:42001"
	request.SetBasicAuth("user", "secret")
	validatorCalls := 0
	_, valid, err := authenticateNativeBasic(request, func(string, string) bool {
		validatorCalls++
		return true
	}, limiter, nil)

	var rateLimited basicAuthRateLimitError
	assert.True(t, errors.As(err, &rateLimited))
	assert.False(t, valid)
	assert.Zero(t, validatorCalls)
}

func TestBasicAuthClientKeyIgnoresSpoofedForwardedForWithoutTrustedProxy(t *testing.T) {
	app := fiber.New(fiber.Config{
		TrustProxy:  true,
		ProxyHeader: fiber.HeaderXForwardedFor,
	})
	var observed, remote string
	app.Get("/", func(c fiber.Ctx) error {
		observed = basicAuthClientKey(c)
		remote = c.RequestCtx().RemoteIP().String()
		return c.SendStatus(fiber.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	request.Header.Set(fiber.HeaderXForwardedFor, "198.51.100.11")
	response, err := app.Test(request)
	require.NoError(t, err)
	assert.Equal(t, fiber.StatusOK, response.StatusCode)
	assert.Equal(t, remote, observed)
	assert.NotEqual(t, "198.51.100.11", observed)
}

func TestAuthenticateNativeBasicCorrectCredentialResetsBeforeCooldown(t *testing.T) {
	limiter := newBasicAuthFailureLimiter()
	validate := func(username, password string) bool {
		return verifyBasicCredential(map[string]string{"user": "secret"}, username, password)
	}
	for range basicAuthFailureLimit - 1 {
		req := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
		req.RemoteAddr = "192.0.2.41:42001"
		req.SetBasicAuth("user", "wrong")
		_, _, err := authenticateNativeBasic(req, validate, limiter, nil)
		require.Error(t, err)
	}

	correct := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	correct.RemoteAddr = "192.0.2.41:42002"
	correct.SetBasicAuth("user", "secret")
	username, valid, err := authenticateNativeBasic(correct, validate, limiter, nil)
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, "user", username)
}

func TestAuthenticateNativeBasicBlocksCorrectCredentialAfterCooldownStarts(t *testing.T) {
	limiter := newBasicAuthFailureLimiter()
	validate := func(username, password string) bool {
		return verifyBasicCredential(map[string]string{"user": "secret"}, username, password)
	}
	for range basicAuthFailureLimit {
		req := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
		req.RemoteAddr = "192.0.2.42:42001"
		req.SetBasicAuth("user", "wrong")
		_, _, err := authenticateNativeBasic(req, validate, limiter, nil)
		require.Error(t, err)
	}

	correct := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	correct.RemoteAddr = "192.0.2.42:42002"
	correct.SetBasicAuth("user", "secret")
	_, valid, err := authenticateNativeBasic(correct, validate, limiter, nil)
	var rateLimited basicAuthRateLimitError
	assert.True(t, errors.As(err, &rateLimited))
	assert.False(t, valid)
}

func TestBasicAuthFailureLimiterIgnoresBearerRequests(t *testing.T) {
	app := fiber.New()
	app.Use(newBasicAuthMiddleware(map[string]string{"user": "secret"}))
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	for range basicAuthFailureLimit + 1 {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.Header.Set(fiber.HeaderAuthorization, "Bearer an-oauth-token")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
		assert.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
	}

	resp := basicAuthTestRequest(t, app, "user", "wrong")
	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode, "Bearer requests must not consume the Basic budget")
}

func basicAuthTestRequest(t *testing.T, app *fiber.App, username, password string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.SetBasicAuth(username, password)
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}
