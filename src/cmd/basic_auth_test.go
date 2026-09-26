package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicAuthFailureLimiterAllowsCorrectCredentialAfterThrottle(t *testing.T) {
	app := fiber.New()
	app.Use(newBasicAuthMiddleware(map[string]string{"user": "secret"}))
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	for attempt := 1; attempt < basicAuthFailureLimit; attempt++ {
		resp := basicAuthTestRequest(t, app, "user", "wrong")
		assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode, "attempt %d", attempt)
		assert.Empty(t, resp.Header.Get(fiber.HeaderRetryAfter))
	}

	resp := basicAuthTestRequest(t, app, "user", "wrong")
	assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get(fiber.HeaderRetryAfter))

	// A correct credential is evaluated before the failure budget is applied,
	// so a valid administrator is never locked out by previous failures.
	resp = basicAuthTestRequest(t, app, "user", "secret")
	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	resp = basicAuthTestRequest(t, app, "user", "wrong")
	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
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

func TestAuthenticateNativeBasicAllowsCorrectCredentialAfterThrottle(t *testing.T) {
	limiter := newBasicAuthFailureLimiter()
	validate := func(username, password string) bool {
		return verifyBasicCredential(map[string]string{"user": "secret"}, username, password)
	}

	for range basicAuthFailureLimit {
		req := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
		req.SetBasicAuth("user", "wrong")
		_, _, err := authenticateNativeBasic(req, validate, limiter)
		require.Error(t, err)
	}

	correct := httptest.NewRequest(http.MethodGet, "/mcp", http.NoBody)
	correct.SetBasicAuth("user", "secret")
	username, valid, err := authenticateNativeBasic(correct, validate, limiter)
	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, "user", username)
}

func basicAuthTestRequest(t *testing.T, app *fiber.App, username, password string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.SetBasicAuth(username, password)
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}
