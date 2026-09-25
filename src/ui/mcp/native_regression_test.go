package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type nativeTestResolver map[string]*whatsapp.DeviceInstance

func (r nativeTestResolver) ResolveDevice(id string) (*whatsapp.DeviceInstance, string, error) {
	if id == "" {
		id = "a"
	}
	d := r[id]
	if d == nil {
		return nil, "", errors.New("unknown device")
	}
	return d, id, nil
}
func regressionPairedDevice(alias, number string) *whatsapp.DeviceInstance {
	jid := types.NewJID(number, types.DefaultUserServer)
	return whatsapp.NewDeviceInstance(alias, &whatsmeow.Client{Store: &store.Device{ID: &jid}}, nil)
}

type nativeTestClient struct {
	base, sid, user, device string
	client                  *http.Client
}

func (c *nativeTestClient) request(t *testing.T, method string, body any) *http.Response {
	t.Helper()
	var b io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		b = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+"/mcp", b)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	if c.sid != "" {
		req.Header.Set("Mcp-Session-Id", c.sid)
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, "test-secret")
	}
	if c.device != "" {
		req.Header.Set("X-Device-Id", c.device)
	}
	resp, err := c.client.Do(req)
	require.NoError(t, err)
	return resp
}
func (c *nativeTestClient) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	resp := c.request(t, http.MethodPost, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, string(raw))
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sid = sid
	}
	var obj map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "data: ") {
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &obj))
				break
			}
		}
	} else {
		require.NoError(t, json.Unmarshal(raw, &obj))
	}
	require.NotNil(t, obj, string(raw))
	return obj
}
func (c *nativeTestClient) call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	obj := c.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	require.Nil(t, obj["error"], obj)
	result, ok := obj["result"].(map[string]any)
	require.True(t, ok, obj)
	return result
}
func (c *nativeTestClient) initialize(t *testing.T) {
	t.Helper()
	obj := c.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "native-test", "version": "1"}})
	require.Nil(t, obj["error"], obj)
	require.NotEmpty(t, c.sid)
	resp := c.request(t, "POST", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	resp.Body.Close()
	require.Equal(t, 202, resp.StatusCode)
}
func regressionFixture(t *testing.T) (*mcpstore.Store, *NativeHandler, *nativeTestClient, *stubSendService) {
	t.Helper()
	data, err := mcpstore.Open(filepath.Join(t.TempDir(), "mcp.db"))
	require.NoError(t, err)
	resolver := nativeTestResolver{"a": regressionPairedDevice("a", "111"), "b": regressionPairedDevice("b", "222")}
	send := &stubSendService{}
	h := NewNativeHandler(Deps{Data: data, Send: send, Chat: &stubChatService{}, User: &stubUserService{}}, resolver, func(r *http.Request) (string, error) {
		u, p, ok := r.BasicAuth()
		if !ok || p != "test-secret" {
			return "", errors.New("bad auth")
		}
		return u, nil
	}, `Basic realm="test"`, []string{"https://wa.example.test"})
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Close(ctx)
		srv.Close()
		_ = data.Close()
	})
	return data, h, &nativeTestClient{base: srv.URL, user: "alice", device: "a", client: &http.Client{Timeout: 4 * time.Second}}, send
}
func TestNativeBinaryRoundTripAndSend(t *testing.T) {
	_, _, c, send := regressionFixture(t)
	c.initialize(t)
	tools := c.rpc(t, "tools/list", map[string]any{})
	serialized, _ := json.Marshal(tools)
	require.Contains(t, string(serialized), "whatsapp_media")
	require.Contains(t, string(serialized), "whatsapp_events")
	require.Contains(t, string(serialized), "request_history")
	raw := []byte("%PDF-1.7\nMCP binary roundtrip")
	res := c.call(t, "whatsapp_media", map[string]any{"action": "upload", "filename": "report.pdf", "mime_type": "application/pdf", "data_base64": base64.StdEncoding.EncodeToString(raw)})
	require.NotEqual(t, true, res["isError"], res)
	meta := res["structuredContent"].(map[string]any)
	id := meta["media_id"].(string)
	uri := meta["uri"].(string)
	read := c.rpc(t, "resources/read", map[string]any{"uri": uri})
	require.Nil(t, read["error"], read)
	content := read["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	require.Equal(t, base64.StdEncoding.EncodeToString(raw), content["blob"])
	require.Equal(t, "application/pdf", content["mimeType"])
	res = c.call(t, "whatsapp_media", map[string]any{"action": "read", "media_id": id, "include_data": true})
	require.Equal(t, base64.StdEncoding.EncodeToString(raw), res["structuredContent"].(map[string]any)["data_base64"])
	res = c.call(t, "whatsapp_send", map[string]any{"type": "document", "phone": "628123456789", "media_id": id, "caption": "Report"})
	require.NotEqual(t, true, res["isError"], res)
	require.NotNil(t, send.lastFile)
	require.NotNil(t, send.lastFile.File)
	file, err := send.lastFile.File.Open()
	require.NoError(t, err)
	defer file.Close()
	got, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, raw, got)
	require.Equal(t, "report.pdf", send.lastFile.File.Filename)
	other := *c
	other.sid = ""
	other.device = "b"
	other.initialize(t)
	res = other.call(t, "whatsapp_media", map[string]any{"action": "read", "media_id": id})
	require.Equal(t, true, res["isError"])
	res = c.call(t, "whatsapp_media", map[string]any{"action": "upload", "filename": "bad.html", "mime_type": "text/html", "data_base64": base64.StdEncoding.EncodeToString([]byte("<html>bad</html>"))})
	require.Equal(t, true, res["isError"])
	obj := c.rpc(t, "tools/call", map[string]any{"name": "whatsapp_send", "arguments": map[string]any{"type": "document", "phone": "628123456789", "media_id": id, "file_url": "https://example.test/file"}})
	require.True(t, obj["error"] != nil || obj["result"].(map[string]any)["isError"] == true, obj)
}
func TestNativeSessionAuthenticationAndIsolation(t *testing.T) {
	_, _, c, _ := regressionFixture(t)
	c.initialize(t)
	for _, mutation := range []func(*nativeTestClient){func(x *nativeTestClient) { x.user = "bob" }, func(x *nativeTestClient) { x.device = "b" }, func(x *nativeTestClient) { x.sid = "nonexistent" }, func(x *nativeTestClient) { x.user = "" }} {
		bad := *c
		mutation(&bad)
		resp := bad.request(t, "POST", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		resp.Body.Close()
		require.NotEqual(t, 200, resp.StatusCode)
	}
	obj := c.rpc(t, "tools/call", map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"device_id": "b"}})
	require.Nil(t, obj["error"])
	require.NotEqual(t, true, obj["result"].(map[string]any)["isError"])
	for _, headers := range []map[string]string{{"Origin": "https://evil.test"}, {"Host": "evil.test"}} {
		req, err := http.NewRequest("POST", c.base+"/mcp", strings.NewReader(`{}`))
		require.NoError(t, err)
		req.SetBasicAuth("alice", "test-secret")
		for k, v := range headers {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		resp, err := c.client.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 403, resp.StatusCode)
	}
	resp := c.request(t, "DELETE", nil)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	resp = c.request(t, "POST", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	resp.Body.Close()
	require.NotEqual(t, 200, resp.StatusCode)
}
func TestNativeNotificationsAndDurableCursor(t *testing.T) {
	data, _, c, _ := regressionFixture(t)
	c.initialize(t)
	obj := c.rpc(t, "resources/subscribe", map[string]any{"uri": mcpstore.EventResource})
	require.Nil(t, obj["error"], obj)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/mcp", nil)
	require.NoError(t, err)
	req.SetBasicAuth("alice", "test-secret")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", c.sid)
	req.Header.Set("X-Device-Id", "a")
	resp, err := c.client.Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	defer resp.Body.Close()
	messages := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "data:") {
				select {
				case messages <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	_, err = data.Append(context.Background(), mcpstore.Event{DeviceID: "222@s.whatsapp.net", Type: "message", MessageID: "other"})
	require.NoError(t, err)
	select {
	case msg := <-messages:
		t.Fatalf("cross-device notification: %s", msg)
	case <-time.After(75 * time.Millisecond):
	}
	e, err := data.Append(context.Background(), mcpstore.Event{DeviceID: "111@s.whatsapp.net", Type: "message", MessageID: "mine"})
	require.NoError(t, err)
	select {
	case msg := <-messages:
		require.Contains(t, msg, "notifications/resources/updated")
		require.Contains(t, msg, mcpstore.EventResource)
	case <-time.After(time.Second):
		t.Fatal("no MCP notification")
	}
	res := c.call(t, "whatsapp_events", map[string]any{"cursor": 0})
	page := res["structuredContent"].(map[string]any)
	require.Equal(t, float64(e.ID), page["next_cursor"])
	require.Len(t, page["events"], 1)
	res = c.call(t, "whatsapp_events", map[string]any{"cursor": e.ID})
	require.Empty(t, res["structuredContent"].(map[string]any)["events"])
	obj = c.rpc(t, "resources/unsubscribe", map[string]any{"uri": mcpstore.EventResource})
	require.Nil(t, obj["error"])
	_, err = data.Append(context.Background(), mcpstore.Event{DeviceID: "111@s.whatsapp.net", Type: "message"})
	require.NoError(t, err)
	select {
	case msg := <-messages:
		t.Fatalf("notification after unsubscribe: %s", msg)
	case <-time.After(75 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE reader did not stop after disconnect")
	}
}

type nativeHistorySpy struct {
	stubChatService
	requested *domainChat.RequestChatHistoryRequest
}

func (s *nativeHistorySpy) RequestChatHistory(ctx context.Context, r domainChat.RequestChatHistoryRequest) (domainChat.RequestChatHistoryResponse, error) {
	s.requested = &r
	return domainChat.RequestChatHistoryResponse{Status: "requested", RequestedCount: r.Count}, nil
}
func TestHistoryToolDispatchAndBounds(t *testing.T) {
	cs := &nativeHistorySpy{}
	h := InitMcpChat(cs, &stubUserService{}, &stubResolver{})
	for _, count := range []int{1, 50, 500} {
		res, err := h.handleChat(deviceCtx(), callReq(map[string]any{"action": "request_history", "chat_jid": "111@s.whatsapp.net", "count": count}))
		require.NoError(t, err)
		require.False(t, res.IsError)
		require.Equal(t, count, cs.requested.Count)
	}
	for _, count := range []int{0, -1, 501} {
		cs.requested = nil
		res, err := h.handleChat(deviceCtx(), callReq(map[string]any{"action": "request_history", "chat_jid": "111@s.whatsapp.net", "count": count}))
		require.NoError(t, err)
		require.True(t, res.IsError)
		require.Nil(t, cs.requested)
	}
}
