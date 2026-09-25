package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	call "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	chat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	storage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	device "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	group "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/group"
	newsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	send "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	user "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
)

type paritySendSpy struct {
	send.ISendUsecase
	mu    sync.Mutex
	scope string
	phone string
	kind  string
	err   error
}

func (s *paritySendSpy) record(ctx context.Context, kind, phone string) (send.GenericResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, _ := whatsapp.DeviceFromContext(ctx)
	s.scope = d.JID()
	s.kind = kind
	s.phone = phone
	return send.GenericResponse{MessageID: "MOCK-NOT-SENT"}, s.err
}
func (s *paritySendSpy) SendText(ctx context.Context, r send.MessageRequest) (send.GenericResponse, error) {
	return s.record(ctx, "text", r.Phone)
}
func (s *paritySendSpy) SendImage(ctx context.Context, r send.ImageRequest) (send.GenericResponse, error) {
	return s.record(ctx, "image", r.Phone)
}
func (s *paritySendSpy) SendVideo(ctx context.Context, r send.VideoRequest) (send.GenericResponse, error) {
	return s.record(ctx, "video", r.Phone)
}
func (s *paritySendSpy) SendPresence(ctx context.Context, r send.PresenceRequest) (send.GenericResponse, error) {
	return s.record(ctx, r.Type, "")
}
func (s *paritySendSpy) SendChatPresence(ctx context.Context, r send.ChatPresenceRequest) (send.GenericResponse, error) {
	return s.record(ctx, r.Action, r.Phone)
}

type parityChatSpy struct {
	chat.IChatUsecase
	pin            *chat.PinChatRequest
	timer          *chat.SetDisappearingTimerRequest
	historyRequest chat.HistoryRequest
}

func (s *parityChatSpy) PinChat(_ context.Context, r chat.PinChatRequest) (chat.PinChatResponse, error) {
	s.pin = &r
	return chat.PinChatResponse{Pinned: r.Pinned}, nil
}
func (s *parityChatSpy) SetDisappearingTimer(_ context.Context, r chat.SetDisappearingTimerRequest) (chat.SetDisappearingTimerResponse, error) {
	s.timer = &r
	return chat.SetDisappearingTimerResponse{}, nil
}
func (s *parityChatSpy) History(_ context.Context, r chat.HistoryRequest) (chat.HistoryResponse, error) {
	s.historyRequest = r
	return chat.HistoryResponse{Action: r.Action, Messages: []chat.HistoryMessage{{ID: "fixture", Content: "not live"}}, Limit: r.Limit, Total: 1, NextOffset: 1, Completeness: "unknown", Asynchronous: r.Action == "request_backfill", BestEffort: true}, nil
}

type parityDeviceSpy struct {
	device.IDeviceUsecase
	added, removed string
}

func (s *parityDeviceSpy) ListDevices(context.Context) ([]device.Device, error) {
	return []device.Device{{ID: "a"}, {ID: "b"}}, nil
}
func (s *parityDeviceSpy) AddDevice(_ context.Context, id string, _ *storage.DeviceWebhookConfig) (*device.Device, error) {
	s.added = id
	return &device.Device{ID: id}, nil
}
func (s *parityDeviceSpy) RemoveDevice(_ context.Context, id string) error {
	s.removed = id
	return nil
}

type parityUserSpy struct {
	user.IUserUsecase
	phone, pushName string
	avatar          bool
}

func (s *parityUserSpy) Info(_ context.Context, r user.InfoRequest) (user.InfoResponse, error) {
	s.phone = r.Phone
	return user.InfoResponse{}, nil
}
func (s *parityUserSpy) Avatar(_ context.Context, r user.AvatarRequest) (user.AvatarResponse, error) {
	s.phone = r.Phone
	return user.AvatarResponse{URL: "https://example.invalid/avatar"}, nil
}
func (s *parityUserSpy) ChangeAvatar(_ context.Context, r user.ChangeAvatarRequest) error {
	s.avatar = r.Avatar != nil
	return nil
}
func (s *parityUserSpy) ChangePushName(_ context.Context, r user.ChangePushNameRequest) error {
	s.pushName = r.PushName
	return nil
}
func (s *parityUserSpy) MyPrivacySetting(context.Context) (user.MyPrivacySettingResponse, error) {
	return user.MyPrivacySettingResponse{Profile: "contacts"}, nil
}
func (s *parityUserSpy) BusinessProfile(_ context.Context, r user.BusinessProfileRequest) (user.BusinessProfileResponse, error) {
	s.phone = r.Phone
	return user.BusinessProfileResponse{}, nil
}
func (s *parityUserSpy) IsOnWhatsApp(_ context.Context, r user.CheckRequest) (user.CheckResponse, error) {
	s.phone = r.Phone
	return user.CheckResponse{IsOnWhatsApp: true}, nil
}
func (s *parityUserSpy) MyListNewsletter(context.Context) (user.MyListNewsletterResponse, error) {
	return user.MyListNewsletterResponse{Data: []types.NewsletterMetadata{{}, {}}}, nil
}

type parityGroupSpy struct {
	group.IGroupUsecase
	photo   bool
	removed bool
}

func (s *parityGroupSpy) SetGroupPhoto(_ context.Context, r group.SetGroupPhotoRequest) (string, error) {
	s.photo = r.Photo != nil
	s.removed = r.Photo == nil
	return "mock-photo", nil
}
func (s *parityGroupSpy) GetGroupParticipants(_ context.Context, r group.GetGroupParticipantsRequest) (group.GetGroupParticipantsResponse, error) {
	return group.GetGroupParticipantsResponse{GroupID: r.GroupID, Participants: []group.GroupParticipant{{JID: "a"}, {JID: "b"}, {JID: "c"}}}, nil
}

type parityNewsletterSpy struct {
	newsletter.INewsletterUsecase
	count, before int
	unfollowed    string
	private       bool
	downloadPath  string
}

func (s *parityNewsletterSpy) GetMessages(_ context.Context, r newsletter.GetMessagesRequest) (newsletter.GetMessagesResponse, error) {
	s.count = r.Count
	s.before = r.Before
	return newsletter.GetMessagesResponse{Data: []newsletter.Message{{ServerID: 3, Text: "fixture"}, {ServerID: 2, Text: "fixture"}}}, nil
}
func (s *parityNewsletterSpy) Unfollow(_ context.Context, r newsletter.UnfollowRequest) error {
	s.unfollowed = r.NewsletterID
	return nil
}
func (s *parityNewsletterSpy) DownloadMedia(_ context.Context, r newsletter.DownloadMediaRequest) (newsletter.DownloadMediaResponse, error) {
	s.private = r.MCPPrivate
	return newsletter.DownloadMediaResponse{Filename: "fixture.pdf", FilePath: s.downloadPath, MediaType: "document"}, nil
}

type parityCallSpy struct {
	call.ICallUsecase
	id, caller string
}

func (s *parityCallSpy) RejectCall(_ context.Context, caller, id string) error {
	s.id = id
	s.caller = caller
	return nil
}

type parityTestSuite struct {
	s          *server.MCPServer
	ctx        context.Context
	data       *mcpstore.Store
	send       *paritySendSpy
	chat       *parityChatSpy
	device     *parityDeviceSpy
	user       *parityUserSpy
	group      *parityGroupSpy
	newsletter *parityNewsletterSpy
	call       *parityCallSpy
}

func newParityTestSuite(t *testing.T) *parityTestSuite {
	t.Helper()
	suite := &parityTestSuite{data: parityStore(t), send: &paritySendSpy{}, chat: &parityChatSpy{}, device: &parityDeviceSpy{}, user: &parityUserSpy{}, group: &parityGroupSpy{}, newsletter: &parityNewsletterSpy{}, call: &parityCallSpy{}}
	d := pairedTestDevice("a", "111")
	suite.ctx = whatsapp.ContextWithDevice(context.Background(), d)
	resolver := nativeTestResolver{"a": d, "b": pairedTestDevice("b", "222")}
	suite.s = NewServer(Deps{Data: suite.data, Send: suite.send, Chat: suite.chat, Device: suite.device, User: suite.user, Group: suite.group, Newsletter: suite.newsletter, Call: suite.call}, resolver)
	return suite
}
func (s *parityTestSuite) invoke(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	require.Nil(t, raw["error"], raw)
	result := raw["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"], result)
	return result
}
func TestParitySchemasAndDispatch(t *testing.T) {
	s := newParityTestSuite(t)
	list := invokeData(t, s.s, s.ctx, "tools/list", map[string]any{})
	tools := list["result"].(map[string]any)["tools"].([]any)
	require.Len(t, tools, 11)
	names := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
	}
	for _, name := range []string{"whatsapp_send", "whatsapp_message", "whatsapp_chat", "whatsapp_group", "whatsapp_app", "whatsapp_media", "whatsapp_events", "whatsapp_history", "whatsapp_profile", "whatsapp_newsletter", "whatsapp_schedule"} {
		require.True(t, names[name], name)
	}
	s.invoke(t, "whatsapp_send", map[string]any{"type": "text", "phone": "222", "message": "fixture"})
	require.Equal(t, "text", s.send.kind)
	s.invoke(t, "whatsapp_send", map[string]any{"type": "presence", "presence": "available"})
	require.Equal(t, "available", s.send.kind)
	s.invoke(t, "whatsapp_send", map[string]any{"type": "chat_presence", "chat_presence": "start", "phone": "222"})
	require.Equal(t, "start", s.send.kind)
	s.invoke(t, "whatsapp_send", map[string]any{"type": "status", "status_type": "text", "message": "fixture"})
	require.Equal(t, "status@broadcast", s.send.phone)
	s.invoke(t, "whatsapp_chat", map[string]any{"action": "pin", "chat_jid": "10@g.us"})
	require.True(t, s.chat.pin.Pinned)
	s.invoke(t, "whatsapp_chat", map[string]any{"action": "unpin", "chat_jid": "10@g.us"})
	require.False(t, s.chat.pin.Pinned)
	s.invoke(t, "whatsapp_chat", map[string]any{"action": "set_disappearing", "chat_jid": "10@g.us", "timer_seconds": 86400})
	require.Equal(t, uint32(86400), s.chat.timer.TimerSeconds)
	s.ctx = context.Background()
	s.invoke(t, "whatsapp_app", map[string]any{"action": "list_devices", "limit": 1})
	s.invoke(t, "whatsapp_app", map[string]any{"action": "add_device", "target_device_id": "new-slot"})
	require.Equal(t, "new-slot", s.device.added)
	s.invoke(t, "whatsapp_app", map[string]any{"action": "remove_device", "target_device_id": "new-slot"})
	require.Equal(t, "new-slot", s.device.removed)
	s.invoke(t, "whatsapp_app", map[string]any{"action": "reject_call", "device_id": "a", "caller_jid": "222@s.whatsapp.net", "call_id": "existing-call"})
	require.Equal(t, "existing-call", s.call.id)
}
func TestParityProfileNewsletterAndPrivateExports(t *testing.T) {
	s := newParityTestSuite(t)
	for _, action := range []string{"get_profile", "get_avatar", "get_privacy", "get_business_profile"} {
		s.invoke(t, "whatsapp_profile", map[string]any{"action": action})
	}
	require.Equal(t, "111@s.whatsapp.net", s.user.phone)
	s.invoke(t, "whatsapp_profile", map[string]any{"action": "update_profile", "push_name": "Fixture"})
	require.Equal(t, "Fixture", s.user.pushName)
	image, err := s.data.Stage(s.ctx, "111@s.whatsapp.net", "fixture.png", "image/png", []byte("\x89PNG\r\n\x1a\nfixture"))
	require.NoError(t, err)
	s.invoke(t, "whatsapp_profile", map[string]any{"action": "update_avatar", "media_id": image.ID})
	require.True(t, s.user.avatar)
	s.invoke(t, "whatsapp_group", map[string]any{"action": "get_photo", "group_id": "10@g.us"})
	require.Equal(t, "10@g.us", s.user.phone)
	s.invoke(t, "whatsapp_group", map[string]any{"action": "set_photo", "group_id": "10@g.us", "media_id": image.ID})
	require.True(t, s.group.photo)
	s.invoke(t, "whatsapp_group", map[string]any{"action": "remove_photo", "group_id": "10@g.us"})
	require.True(t, s.group.removed)
	result := s.invoke(t, "whatsapp_group", map[string]any{"action": "export_participants", "group_id": "10@g.us", "limit": 1, "offset": 1})
	meta := result["structuredContent"].(map[string]any)
	require.Equal(t, true, meta["has_more"])
	media := meta["media"].(map[string]any)
	_, raw, err := s.data.Media(s.ctx, "111@s.whatsapp.net", media["media_id"].(string))
	require.NoError(t, err)
	var export map[string]any
	require.NoError(t, json.Unmarshal(raw, &export))
	require.Len(t, export["data"], 1)
	require.Equal(t, "b", export["data"].([]any)[0].(map[string]any)["jid"])
	s.invoke(t, "whatsapp_newsletter", map[string]any{"action": "list", "limit": 1})
	result = s.invoke(t, "whatsapp_newsletter", map[string]any{"action": "get_messages", "newsletter_id": "1@newsletter", "count": 2, "before": 4})
	require.Equal(t, 2, s.newsletter.count)
	require.Equal(t, 4, s.newsletter.before)
	require.Equal(t, float64(2), result["structuredContent"].(map[string]any)["next_before"])
	s.invoke(t, "whatsapp_newsletter", map[string]any{"action": "unfollow", "newsletter_id": "1@newsletter"})
	require.Equal(t, "1@newsletter", s.newsletter.unfollowed)
	old := config.McpDataDir
	config.McpDataDir = t.TempDir()
	t.Cleanup(func() { config.McpDataDir = old })
	directory := filepath.Join(config.McpDataDir, "downloads")
	require.NoError(t, os.MkdirAll(directory, 0700))
	s.newsletter.downloadPath = filepath.Join(directory, "fixture.pdf")
	require.NoError(t, os.WriteFile(s.newsletter.downloadPath, []byte("%PDF-1.7\nfixture"), 0600))
	result = s.invoke(t, "whatsapp_newsletter", map[string]any{"action": "download_media", "newsletter_id": "1@newsletter", "server_id": 3})
	require.True(t, s.newsletter.private)
	require.Len(t, result["content"], 2)
	_, err = os.Stat(s.newsletter.downloadPath)
	require.True(t, os.IsNotExist(err))
	result = s.invoke(t, "whatsapp_history", map[string]any{"action": "export", "limit": 1})
	require.NotNil(t, result["structuredContent"].(map[string]any)["media"])
	s.invoke(t, "whatsapp_history", map[string]any{"action": "request_backfill", "chat_jid": "10@g.us", "count": 100})
	require.Equal(t, 100, s.chat.historyRequest.Count)
}
func TestParityInvalidActionsAndBounds(t *testing.T) {
	s := newParityTestSuite(t)
	cases := []struct {
		name string
		args map[string]any
	}{
		{"whatsapp_history", map[string]any{"action": "search_all", "limit": 501}},
		{"whatsapp_history", map[string]any{"action": "context", "chat_jid": "10@g.us"}},
		{"whatsapp_history", map[string]any{"action": "coverage"}},
		{"whatsapp_history", map[string]any{"action": "export", "offset": -1}},
		{"whatsapp_send", map[string]any{"type": "presence", "presence": "invalid"}},
		{"whatsapp_send", map[string]any{"type": "chat_presence", "chat_presence": "start"}},
		{"whatsapp_send", map[string]any{"type": "status", "status_type": "text"}},
		{"whatsapp_send", map[string]any{"type": "status", "status_type": "text", "message": "x", "phone": "222"}},
		{"whatsapp_chat", map[string]any{"action": "set_disappearing", "chat_jid": "10@g.us", "timer_seconds": -1}},
		{"whatsapp_newsletter", map[string]any{"action": "get_messages", "newsletter_id": "1@newsletter", "count": 101}},
		{"whatsapp_newsletter", map[string]any{"action": "download_media", "newsletter_id": "1@newsletter", "server_id": 2147483647}},
		{"whatsapp_newsletter", map[string]any{"action": "publish", "newsletter_id": "1@newsletter"}},
		{"whatsapp_profile", map[string]any{"action": "update_profile", "push_name": "x", "media_id": "00000000-0000-0000-0000-000000000000"}},
		{"whatsapp_profile", map[string]any{"action": "update_privacy"}},
		{"whatsapp_group", map[string]any{"action": "set_photo", "group_id": "10@g.us"}},
		{"whatsapp_app", map[string]any{"action": "add_device"}},
		{"whatsapp_app", map[string]any{"action": "initiate_call"}},
		{"whatsapp_app", map[string]any{"action": "accept_call"}},
		{"whatsapp_history", map[string]any{"action": "search_all", "device_id": "missing"}},
	}
	for _, tc := range cases {
		raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": tc.name, "arguments": tc.args})
		if raw["error"] == nil {
			require.Equal(t, true, raw["result"].(map[string]any)["isError"], tc)
		}
	}
	s.send.err = errors.New("fixture send failure")
	raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": "whatsapp_send", "arguments": map[string]any{"type": "presence", "presence": "available"}})
	require.Equal(t, true, raw["result"].(map[string]any)["isError"])
}
func TestNativePerCallOverrideDoesNotRetargetSubscription(t *testing.T) {
	data := parityStore(t)
	resolver := nativeTestResolver{"a": pairedTestDevice("a", "111"), "b": pairedTestDevice("b", "222")}
	sendSpy := &paritySendSpy{}
	h := NewNativeHandler(Deps{Data: data, Send: sendSpy}, resolver, func(r *http.Request) (string, error) {
		u, p, ok := r.BasicAuth()
		if !ok || p != "test-password" {
			return "", errors.New("denied")
		}
		return u, nil
	}, `Basic realm="test"`, []string{"https://trusted.example"})
	srv := httptest.NewServer(h)
	t.Cleanup(func() { _ = h.Close(context.Background()); srv.Close() })
	sid := initializeNative(t, srv)
	result, _ := nativeRPC(t, srv.Client(), srv.URL, "alice", sid, "a", "tools/call", map[string]any{"name": "whatsapp_send", "arguments": map[string]any{"type": "presence", "presence": "available", "device_id": "b"}})
	require.Nil(t, result["error"])
	require.NotEqual(t, true, result["result"].(map[string]any)["isError"])
	sendSpy.mu.Lock()
	require.Equal(t, "222@s.whatsapp.net", sendSpy.scope)
	sendSpy.mu.Unlock()
	h.mu.Lock()
	require.Equal(t, "111@s.whatsapp.net", h.sessions[sid].identity.device)
	h.mu.Unlock()
	bad := nativeRequest(t, srv.Client(), srv.URL, "POST", "bob", sid, "a", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	defer bad.Body.Close()
	require.Equal(t, 404, bad.StatusCode)
}
func TestExportParitySchemas(t *testing.T) {
	path := os.Getenv("GOWA_SCHEMA_EXPORT")
	if path == "" {
		t.Skip("schema export is opt-in during packaging")
	}
	s := newParityTestSuite(t)
	result := invokeData(t, s.s, s.ctx, "tools/list", map[string]any{})
	raw, err := json.MarshalIndent(result["result"], "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0644))
}
