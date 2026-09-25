package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	mcpg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func pairedTestDevice(alias, number string) *whatsapp.DeviceInstance {
	jid := types.NewJID(number, types.DefaultUserServer)
	return whatsapp.NewDeviceInstance(alias, &whatsmeow.Client{Store: &wastore.Device{ID: &jid}}, nil)
}
func parityStore(t *testing.T) *mcpstore.Store {
	t.Helper()
	s, err := mcpstore.Open(filepath.Join(t.TempDir(), "mcp.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}
func invokeData(t *testing.T, s *server.MCPServer, ctx context.Context, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)
	raw, err := json.Marshal(s.HandleMessage(ctx, body))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
func TestAttachmentMCPRoundTripAndSend(t *testing.T) {
	data := parityStore(t)
	d := pairedTestDevice("a", "62811")
	ctx := whatsapp.ContextWithDevice(context.Background(), d)
	resolver := &stubResolver{inst: d}
	send := &stubSendService{}
	s := NewServer(Deps{Data: data, Send: send}, resolver)
	content := []byte("%PDF-1.7\nprivate report")
	upload := invokeData(t, s, ctx, "tools/call", map[string]any{"name": "whatsapp_media", "arguments": map[string]any{"action": "upload", "filename": "report.pdf", "mime_type": "application/pdf", "data_base64": base64.StdEncoding.EncodeToString(content)}})
	require.Nil(t, upload["error"])
	result := upload["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"])
	meta := result["structuredContent"].(map[string]any)
	id := meta["media_id"].(string)
	read := invokeData(t, s, ctx, "resources/read", map[string]any{"uri": meta["uri"]})
	require.Nil(t, read["error"])
	contents := read["result"].(map[string]any)["contents"].([]any)
	require.Equal(t, base64.StdEncoding.EncodeToString(content), contents[0].(map[string]any)["blob"])
	fallback := invokeData(t, s, ctx, "tools/call", map[string]any{"name": "whatsapp_media", "arguments": map[string]any{"action": "read", "media_id": id, "include_data": true}})
	require.Equal(t, base64.StdEncoding.EncodeToString(content), fallback["result"].(map[string]any)["structuredContent"].(map[string]any)["data_base64"])
	other := whatsapp.ContextWithDevice(context.Background(), pairedTestDevice("b", "62822"))
	denied := invokeData(t, s, other, "resources/read", map[string]any{"uri": meta["uri"]})
	require.NotNil(t, denied["error"])
	sent, err := InitMcpSend(send, resolver, data).handleSend(ctx, callReq(map[string]any{"type": "document", "phone": "62833", "media_id": id}))
	require.NoError(t, err)
	require.False(t, sent.IsError)
	require.NotNil(t, send.lastFile.File)
	file, err := send.lastFile.File.Open()
	require.NoError(t, err)
	defer file.Close()
	got, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, content, got)
	_, cleanup, err := stagedUpload(ctx, callReq(map[string]any{"media_id": id, "file_url": "https://example.com/x"}), data, "document")
	defer cleanup()
	require.Error(t, err)
	_, cleanup, err = stagedUpload(other, callReq(map[string]any{"media_id": id}), data, "document")
	defer cleanup()
	require.Error(t, err)
	_, cleanup, err = stagedUpload(ctx, callReq(map[string]any{"media_id": id}), data, "image")
	defer cleanup()
	require.Error(t, err)
}
func TestInlineImageAndUnsafeUploads(t *testing.T) {
	data := parityStore(t)
	d := pairedTestDevice("a", "62811")
	ctx := whatsapp.ContextWithDevice(context.Background(), d)
	s := NewServer(Deps{Data: data}, &stubResolver{inst: d})
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWZkAAAAASUVORK5CYII=")
	require.NoError(t, err)
	m, err := data.Stage(ctx, d.JID(), "x.png", "image/png", png)
	require.NoError(t, err)
	result := mediaResult(m, png, true, false)
	require.IsType(t, mcpg.ImageContent{}, result.Content[1])
	for _, args := range []map[string]any{
		{"action": "upload", "filename": "x.pdf", "mime_type": "application/pdf", "data_base64": "!bad!"},
		{"action": "upload", "filename": "../x.pdf", "mime_type": "application/pdf", "data_base64": "eA=="},
		{"action": "upload", "filename": "x.html", "mime_type": "text/html", "data_base64": "eA=="},
	} {
		r := invokeData(t, s, ctx, "tools/call", map[string]any{"name": "whatsapp_media", "arguments": args})
		if r["error"] == nil {
			require.Equal(t, true, r["result"].(map[string]any)["isError"])
		}
	}
}

type historySpy struct {
	domainChat.IChatUsecase
	got   domainChat.RequestChatHistoryRequest
	calls int
}

func (h *historySpy) RequestChatHistory(_ context.Context, r domainChat.RequestChatHistoryRequest) (domainChat.RequestChatHistoryResponse, error) {
	h.got = r
	h.calls++
	return domainChat.RequestChatHistoryResponse{}, nil
}
func TestRequestHistoryMCPDispatchAndBounds(t *testing.T) {
	spy := &historySpy{}
	h := InitMcpChat(spy, nil, &stubResolver{})
	r, err := h.handleChat(deviceCtx(), callReq(map[string]any{"action": "request_history", "chat_jid": "62833@s.whatsapp.net", "count": 125}))
	require.NoError(t, err)
	require.False(t, r.IsError)
	require.Equal(t, 125, spy.got.Count)
	for _, n := range []int{0, -1, 501} {
		r, err = h.handleChat(deviceCtx(), callReq(map[string]any{"action": "request_history", "chat_jid": "62833@s.whatsapp.net", "count": n}))
		require.NoError(t, err)
		require.True(t, r.IsError)
	}
	require.Equal(t, 1, spy.calls)
	s := NewServer(Deps{Chat: spy}, &stubResolver{})
	invalid := invokeData(t, s, deviceCtx(), "tools/call", map[string]any{"name": "whatsapp_chat", "arguments": map[string]any{"action": "request_history", "count": 10}})
	if invalid["error"] == nil {
		require.Equal(t, true, invalid["result"].(map[string]any)["isError"])
	}
	require.Equal(t, 1, spy.calls)
}
func TestPrivateMediaDirectory(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "statics")
	require.NoError(t, os.MkdirAll(public, 0700))
	require.NoError(t, ValidatePrivateDataDir(filepath.Join(root, "private"), public))
	require.Error(t, ValidatePrivateDataDir(filepath.Join(public, "uploads"), public))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(public, link))
	require.Error(t, ValidatePrivateDataDir(filepath.Join(link, "uploads"), public))
}
