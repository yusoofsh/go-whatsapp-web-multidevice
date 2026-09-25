package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/require"
)

type parityResolver struct{ a, b *whatsapp.DeviceInstance }

func (r *parityResolver) ResolveDevice(id string) (*whatsapp.DeviceInstance, string, error) {
	switch id {
	case "", "a":
		return r.a, "a", nil
	case "b":
		return r.b, "b", nil
	default:
		return nil, "", errors.New("unknown device")
	}
}
func nativeRequest(t *testing.T, client *http.Client, base, method, user, session, device string, body []byte) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, base+"/mcp", bytes.NewReader(body))
	require.NoError(t, err)
	r.SetBasicAuth(user, "test-password")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	if device != "" {
		r.Header.Set("X-Device-Id", device)
	}
	response, err := client.Do(r)
	require.NoError(t, err)
	return response
}
func nativeRPC(t *testing.T, client *http.Client, base, user, session, device, method string, params any) (map[string]any, http.Header) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)
	response := nativeRequest(t, client, base, http.MethodPost, user, session, device, body)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode, string(raw))
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "data: ") {
				raw = []byte(strings.TrimPrefix(line, "data: "))
				break
			}
		}
	}
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return out, response.Header
}
func nativeFixture(t *testing.T) (*NativeHandler, *httptest.Server, *mcpstore.Store, *parityResolver) {
	t.Helper()
	data := parityStore(t)
	resolver := &parityResolver{pairedTestDevice("a", "62811"), pairedTestDevice("b", "62822")}
	auth := func(r *http.Request) (string, error) {
		user, password, ok := r.BasicAuth()
		if !ok || password != "test-password" || (user != "alice" && user != "bob") {
			return "", errors.New("denied")
		}
		return user, nil
	}
	h := NewNativeHandler(Deps{Data: data}, resolver, auth, `Basic realm="test"`, []string{"https://trusted.example"})
	t.Cleanup(func() { require.NoError(t, h.Close(context.Background())) })
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv, data, resolver
}
func initializeNative(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	result, headers := nativeRPC(t, srv.Client(), srv.URL, "alice", "", "a", "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "parity-test", "version": "1"}})
	require.Nil(t, result["error"])
	id := headers.Get("Mcp-Session-Id")
	require.NotEmpty(t, id)
	response := nativeRequest(t, srv.Client(), srv.URL, "POST", "alice", id, "a", []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	response.Body.Close()
	require.Equal(t, 202, response.StatusCode)
	return id
}
func TestNativeMCPStreamingAndOwnership(t *testing.T) {
	_, srv, data, resolver := nativeFixture(t)
	session := initializeNative(t, srv)
	for _, test := range []struct{ method, user, device, id string }{
		{"GET", "bob", "a", session}, {"POST", "bob", "a", session}, {"GET", "alice", "b", session}, {"GET", "alice", "a", "unknown"}, {"GET", "alice", "a", ""},
	} {
		response := nativeRequest(t, srv.Client(), srv.URL, test.method, test.user, test.id, test.device, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
		response.Body.Close()
		require.Equal(t, 404, response.StatusCode, "session ownership on %s", test.method)
	}
	// Establish an actual client-cancellable GET stream before subscribing.
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/mcp", nil)
	require.NoError(t, err)
	request.SetBasicAuth("alice", "test-password")
	request.Header.Set("Mcp-Session-Id", session)
	request.Header.Set("X-Device-Id", "a")
	request.Header.Set("Accept", "text/event-stream")
	stream, err := srv.Client().Do(request)
	require.NoError(t, err)
	defer stream.Body.Close()
	require.Equal(t, 200, stream.StatusCode)
	notifications := make(chan string, 16)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "notifications/resources/updated") {
				select {
				case notifications <- line:
				case <-streamCtx.Done():
					return
				}
			}
		}
	}()
	result, _ := nativeRPC(t, srv.Client(), srv.URL, "alice", session, "a", "resources/subscribe", map[string]any{"uri": mcpstore.EventResource})
	require.Nil(t, result["error"])
	_, err = data.Append(context.Background(), mcpstore.Event{DeviceID: resolver.b.JID(), Type: "message", MessageID: "other-device"})
	require.NoError(t, err)
	select {
	case <-notifications:
		t.Fatal("cross-device notification leaked")
	case <-time.After(80 * time.Millisecond):
	}
	event, err := data.Append(context.Background(), mcpstore.Event{DeviceID: resolver.a.JID(), Type: "message", MessageID: "local-message"})
	require.NoError(t, err)
	select {
	case line := <-notifications:
		require.Contains(t, line, mcpstore.EventResource)
		require.NotContains(t, line, "local-message")
	case <-time.After(3 * time.Second):
		t.Fatal("missing live MCP notification")
	}
	result, _ = nativeRPC(t, srv.Client(), srv.URL, "alice", session, "a", "tools/call", map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"cursor": 0}})
	require.Nil(t, result["error"])
	page := result["result"].(map[string]any)["structuredContent"].(map[string]any)
	require.Equal(t, float64(event.ID), page["next_cursor"])
	require.Len(t, page["events"], 1)
	result, _ = nativeRPC(t, srv.Client(), srv.URL, "alice", session, "a", "tools/call", map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"cursor": event.ID}})
	require.Len(t, result["result"].(map[string]any)["structuredContent"].(map[string]any)["events"], 0)
	result, _ = nativeRPC(t, srv.Client(), srv.URL, "alice", session, "a", "resources/unsubscribe", map[string]any{"uri": mcpstore.EventResource})
	require.Nil(t, result["error"])
	_, err = data.Append(context.Background(), mcpstore.Event{DeviceID: resolver.a.JID(), Type: "message.edited"})
	require.NoError(t, err)
	select {
	case <-notifications:
		t.Fatal("notification after unsubscribe")
	case <-time.After(80 * time.Millisecond):
	}
	result, _ = nativeRPC(t, srv.Client(), srv.URL, "alice", session, "a", "tools/call", map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"device_id": "b"}})
	require.Nil(t, result["error"])
	require.NotEqual(t, true, result["result"].(map[string]any)["isError"])
	cancel()
	stream.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("SSE did not close on client disconnect")
	}
	response := nativeRequest(t, srv.Client(), srv.URL, "DELETE", "alice", session, "a", nil)
	response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	response = nativeRequest(t, srv.Client(), srv.URL, "GET", "alice", session, "a", nil)
	response.Body.Close()
	require.Equal(t, 404, response.StatusCode)
}
func TestNativeOriginAuthAndBodyLimit(t *testing.T) {
	_, srv, _, _ := nativeFixture(t)
	request, err := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	response, err := srv.Client().Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 401, response.StatusCode)
	require.NotEmpty(t, response.Header.Get("WWW-Authenticate"))
	request, err = http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.SetBasicAuth("alice", "test-password")
	request.Header.Set("Origin", "https://evil.example")
	response, err = srv.Client().Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 403, response.StatusCode)
	response = nativeRequest(t, srv.Client(), srv.URL, "POST", "alice", "", "a", bytes.Repeat([]byte("x"), mcpstore.MaxRequestBytes+1))
	response.Body.Close()
	require.Equal(t, 413, response.StatusCode)
}
