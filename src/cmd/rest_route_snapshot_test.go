package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// cmd tests share these globals and do not run in parallel.
type routeSnapshotConfig struct {
	chatwootEnabled     bool
	mcpEnabled          bool
	mcpOAuthEnabled     bool
	mcpOAuthIssuerURL   string
	mcpOAuthResourceURL string
	mcpOAuthDBURI       string
	appBasicAuth        []string
	appUIEnabled        bool
	appUIAutoUpdate     bool
	appBasePath         string
	appDebug            bool
}

func currentRouteSnapshotConfig() routeSnapshotConfig {
	return routeSnapshotConfig{
		chatwootEnabled:     config.ChatwootEnabled,
		mcpEnabled:          config.McpEnabled,
		mcpOAuthEnabled:     config.McpOAuthEnabled,
		mcpOAuthIssuerURL:   config.McpOAuthIssuerURL,
		mcpOAuthResourceURL: config.McpOAuthResourceURL,
		mcpOAuthDBURI:       config.McpOAuthDBURI,
		appBasicAuth:        append([]string(nil), config.AppBasicAuthCredential...),
		appUIEnabled:        config.AppUIEnabled,
		appUIAutoUpdate:     config.AppUIAutoUpdate,
		appBasePath:         config.AppBasePath,
		appDebug:            config.AppDebug,
	}
}

func (saved routeSnapshotConfig) restore() {
	config.ChatwootEnabled = saved.chatwootEnabled
	config.McpEnabled = saved.mcpEnabled
	config.McpOAuthEnabled = saved.mcpOAuthEnabled
	config.McpOAuthIssuerURL = saved.mcpOAuthIssuerURL
	config.McpOAuthResourceURL = saved.mcpOAuthResourceURL
	config.McpOAuthDBURI = saved.mcpOAuthDBURI
	config.AppBasicAuthCredential = saved.appBasicAuth
	config.AppUIEnabled = saved.appUIEnabled
	config.AppUIAutoUpdate = saved.appUIAutoUpdate
	config.AppBasePath = saved.appBasePath
	config.AppDebug = saved.appDebug
}

func useRouteSnapshotConfig(t *testing.T, apply func()) {
	t.Helper()
	saved := currentRouteSnapshotConfig()
	t.Cleanup(saved.restore)
	apply()
}

type routeProbe struct {
	status int
	auth   string
	body   string
}

func probeRoute(t *testing.T, app *fiber.App, method, target, user, password string) routeProbe {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	got := routeProbe{
		status: resp.StatusCode,
		auth:   strings.Join(resp.Header.Values(fiber.HeaderWWWAuthenticate), ", "),
		body:   string(raw),
	}
	shown := got.body
	if len(shown) > 180 {
		shown = shown[:180]
	}
	t.Logf("probe %s %s status=%d auth=%q body=%q", method, target, got.status, got.auth, shown)
	return got
}

func TestRouteSnapshot(t *testing.T) {
	t.Run("device group", func(t *testing.T) {
		useRouteSnapshotConfig(t, func() {
			config.ChatwootEnabled = true
			config.McpEnabled = true
			config.McpOAuthEnabled = false
			config.AppBasicAuthCredential = nil
			config.AppUIEnabled = false
			config.AppUIAutoUpdate = false
			config.AppBasePath = ""
			config.AppDebug = false
		})

		app, _, cleanup, err := buildRESTApp(nil)
		require.NoError(t, err)
		t.Cleanup(cleanup)

		root := probeRoute(t, app, http.MethodGet, "/", "", "")
		require.Equal(t, http.StatusOK, root.status)
		require.Contains(t, root.body, "UI disabled")
		require.NotContains(t, root.body, "DEVICE_MANAGER_UNAVAILABLE")

		// GET /mcp has no handler. The request falls through to the device
		// group, which is how inherited middleware shows up on this path.
		mcp := probeRoute(t, app, http.MethodGet, "/mcp", "", "")
		require.Equal(t, http.StatusServiceUnavailable, mcp.status)
		require.Contains(t, mcp.body, "DEVICE_MANAGER_UNAVAILABLE")
		require.NotContains(t, mcp.auth, "Bearer")

		// "/" is on DeviceMiddleware's allow list, so this route is the one
		// that shows a registration after the device group.
		configs := probeRoute(t, app, http.MethodGet, "/chatwoot/configs", "", "")
		require.Equal(t, http.StatusBadRequest, configs.status)
		require.Contains(t, configs.body, "storage not available")
		require.NotContains(t, configs.body, "DEVICE_MANAGER_UNAVAILABLE")
		t.Log("route snapshot uses fiber app.Test; no listener bind")
	})

	t.Run("oauth mount", func(t *testing.T) {
		useRouteSnapshotConfig(t, func() {
			config.ChatwootEnabled = false
			config.McpEnabled = true
			config.McpOAuthEnabled = true
			config.McpOAuthIssuerURL = "https://gowa.example.com"
			config.McpOAuthResourceURL = ""
			config.McpOAuthDBURI = "file:route-snapshot-oauth?mode=memory&cache=shared"
			config.AppBasicAuthCredential = []string{"user:secret"}
			config.AppUIEnabled = false
			config.AppUIAutoUpdate = false
			config.AppBasePath = ""
			config.AppDebug = false
		})

		app, _, cleanup, err := buildRESTApp(nil)
		require.NoError(t, err)
		t.Cleanup(cleanup)

		root := probeRoute(t, app, http.MethodGet, "/", "", "")
		require.Equal(t, http.StatusUnauthorized, root.status)
		require.NotContains(t, root.auth, "Bearer")
		require.Contains(t, root.auth, `Basic realm="Restricted"`)

		mcp := probeRoute(t, app, http.MethodGet, "/mcp", "", "")
		require.Equal(t, http.StatusUnauthorized, mcp.status)
		require.Contains(t, mcp.auth, "Bearer")
		require.Contains(t, mcp.auth, "resource_metadata")
		t.Log("route snapshot uses fiber app.Test; no listener bind")
	})
}
