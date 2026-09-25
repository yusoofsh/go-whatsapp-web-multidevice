package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	chat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	group "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/group"
	message "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	newsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	send "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	user "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	mcpg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type parityHandler struct {
	deps     Deps
	resolver deviceResolver
}

func parityResult(value any, err error) (*mcpg.CallToolResult, error) {
	if err != nil {
		return mcpg.NewToolResultError(err.Error()), nil
	}
	return mcpg.NewToolResultStructuredOnly(value), nil
}
func parityOK(err error) (*mcpg.CallToolResult, error) {
	return parityResult(map[string]any{"status": "success"}, err)
}
func parityError(text string) (*mcpg.CallToolResult, error) {
	return mcpg.NewToolResultError(text), nil
}
func addParityTool(s *server.MCPServer, name, description string, schema json.RawMessage, destructive bool, handler func(context.Context, mcpg.CallToolRequest) (*mcpg.CallToolResult, error)) {
	tool := mcpg.NewTool(name, mcpg.WithDescription(description), mcpg.WithReadOnlyHintAnnotation(false), mcpg.WithDestructiveHintAnnotation(destructive), mcpg.WithIdempotentHintAnnotation(false), mcpg.WithRawInputSchema(schema))
	tool.InputSchema = mcpg.ToolInputSchema{}
	s.AddTool(tool, handler)
}

// Re-register only the existing consolidated names that gain actions. Every old
// action delegates to its original handler, including future upstream additions.
func registerParityTools(s *server.MCPServer, deps Deps, resolver deviceResolver) {
	h := &parityHandler{deps: deps, resolver: resolver}
	addParityTool(s, "whatsapp_send", "Send text/media and existing message types. Additional types: presence (available/unavailable), chat_presence (start/stop), status (status_type text/image/video; broadcasts to the account's existing status privacy audience, not a single recipient).", paritySendSchema(), false, h.send)
	addParityTool(s, "whatsapp_chat", "Existing per-chat queries, archive and request_history; also pin, unpin, set_disappearing (0/86400/604800/7776000 seconds). Use whatsapp_history for cross-chat archives.", parityChatSchema(), false, h.chat)
	addParityTool(s, "whatsapp_group", "Existing group actions plus get_photo, set_photo from a staged media_id, remove_photo and bounded JSON export_participants. Removing photos or members is destructive.", parityGroupSchema(), true, h.group)
	addParityTool(s, "whatsapp_app", "Existing session actions plus list_devices, add_device, remove_device using target_device_id. reject_call handles an existing incoming call only; no voice/video initiation or acceptance.", parityAppSchema(), true, h.app)
	addParityTool(s, "whatsapp_history", "Device-scoped local archive: search_all with combined filters, chronological context, bounded JSON export, coverage and asynchronous best-effort request_backfill. Stored coverage never proves complete phone history. Text-preferring clients receive JSON results.", json.RawMessage(historySchema), false, h.history)
	addParityTool(s, "whatsapp_newsletter", "List subscribed channels, get_messages with count/before pagination, private download_media by server_id, and unfollow. Follow/join and publishing are not exposed by the pinned GoWA REST usecase.", json.RawMessage(newsletterSchema), true, h.newsletter)
	addParityTool(s, "whatsapp_profile", "Get profile/avatar/business profile/privacy. Update own push name or avatar with staged media_id; update_profile accepts exactly one of those fields. Privacy and business-profile writes are unsupported. phone defaults to the selected account for reads.", json.RawMessage(profileSchema), false, h.profile)
}

func pageBounds(total, limit, offset int) (int, int, error) {
	if limit < 1 || limit > 500 || offset < 0 || offset > 100000 {
		return 0, 0, errors.New("invalid pagination bounds")
	}
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return start, end, nil
}
func pageResult[T any](items []T, limit, offset int) (map[string]any, error) {
	start, end, err := pageBounds(len(items), limit, offset)
	if err != nil {
		return nil, err
	}
	page := append([]T{}, items[start:end]...)
	return map[string]any{"data": page, "limit": limit, "offset": offset, "total": len(items), "next_offset": offset + len(page), "has_more": end < len(items)}, nil
}
func requiredText(r mcpg.CallToolRequest, key string) (string, error) {
	v, err := r.RequireString(key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(v) == "" || len(v) > 256 {
		return "", fmt.Errorf("%s must contain 1..256 bytes", key)
	}
	return v, nil
}

func (h *parityHandler) send(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	kind := r.GetString("type", "")
	legacy := InitMcpSend(h.deps.Send, h.resolver, h.deps.Data)
	if kind != "presence" && kind != "chat_presence" && kind != "status" {
		return legacy.handleSend(ctx, r)
	}
	ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	if h.deps.Send == nil {
		return parityError("send service unavailable")
	}
	switch kind {
	case "presence":
		presence := r.GetString("presence", "")
		if presence != "available" && presence != "unavailable" {
			return parityError("presence must be available or unavailable")
		}
		value, err := h.deps.Send.SendPresence(ctx, send.PresenceRequest{Type: presence})
		return parityResult(value, err)
	case "chat_presence":
		phone, err := requiredText(r, "phone")
		if err != nil {
			return parityResult(nil, err)
		}
		action := r.GetString("chat_presence", "")
		if action != "start" && action != "stop" {
			return parityError("chat_presence must be start or stop")
		}
		value, err := h.deps.Send.SendChatPresence(ctx, send.ChatPresenceRequest{Phone: phone, Action: action})
		return parityResult(value, err)
	default:
		statusType := r.GetString("status_type", "")
		if statusType != "text" && statusType != "image" && statusType != "video" {
			return parityError("status_type must be text, image or video")
		}
		if phone := r.GetString("phone", ""); phone != "" && phone != "status@broadcast" {
			return parityError("status broadcasts cannot specify a single recipient")
		}
		args := make(map[string]any, len(r.GetArguments())+1)
		for k, v := range r.GetArguments() {
			args[k] = v
		}
		args["type"] = statusType
		args["phone"] = "status@broadcast"
		r.Params.Arguments = args
		return legacy.handleSend(ctx, r)
	}
}
func (h *parityHandler) chat(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	action := r.GetString("action", "")
	if action != "pin" && action != "unpin" && action != "set_disappearing" {
		return InitMcpChat(h.deps.Chat, h.deps.User, h.resolver).handleChat(ctx, r)
	}
	ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	if h.deps.Chat == nil {
		return parityError("chat service unavailable")
	}
	jid, err := requiredText(r, "chat_jid")
	if err != nil {
		return parityResult(nil, err)
	}
	if action == "set_disappearing" {
		timer := r.GetInt("timer_seconds", -1)
		if timer != 0 && timer != 86400 && timer != 604800 && timer != 7776000 {
			return parityError("invalid disappearing timer")
		}
		value, err := h.deps.Chat.SetDisappearingTimer(ctx, chat.SetDisappearingTimerRequest{ChatJID: jid, TimerSeconds: uint32(timer)})
		return parityResult(value, err)
	}
	pinned := action == "pin"
	if action == "pin" {
		pinned = r.GetBool("pinned", true)
	}
	value, err := h.deps.Chat.PinChat(ctx, chat.PinChatRequest{ChatJID: jid, Pinned: pinned})
	return parityResult(value, err)
}
func (h *parityHandler) app(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	action := r.GetString("action", "")
	if action == "list_devices" || action == "add_device" || action == "remove_device" {
		if h.deps.Device == nil {
			return parityError("device management service unavailable")
		}
		if action == "list_devices" {
			devices, err := h.deps.Device.ListDevices(ctx)
			if err != nil {
				return parityResult(nil, err)
			}
			page, err := pageResult(devices, r.GetInt("limit", 50), r.GetInt("offset", 0))
			return parityResult(page, err)
		}
		id, err := requiredText(r, "target_device_id")
		if err != nil {
			return parityResult(nil, err)
		}
		if action == "add_device" {
			value, err := h.deps.Device.AddDevice(ctx, id, nil)
			return parityResult(value, err)
		}
		return parityOK(h.deps.Device.RemoveDevice(ctx, id))
	}
	if action == "reject_call" {
		ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
		if err != nil {
			return parityResult(nil, err)
		}
		if h.deps.Call == nil {
			return parityError("incoming-call rejection service unavailable")
		}
		caller, err := requiredText(r, "caller_jid")
		if err != nil {
			return parityResult(nil, err)
		}
		id, err := requiredText(r, "call_id")
		if err != nil {
			return parityResult(nil, err)
		}
		return parityOK(h.deps.Call.RejectCall(ctx, caller, id))
	}
	return InitMcpApp(h.deps.App, h.resolver).handleApp(ctx, r)
}
func (h *parityHandler) group(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	action := r.GetString("action", "")
	if action != "get_photo" && action != "set_photo" && action != "remove_photo" && action != "export_participants" {
		return InitMcpGroup(h.deps.Group, h.resolver).handleGroup(ctx, r)
	}
	ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	id, err := requiredText(r, "group_id")
	if err != nil {
		return parityResult(nil, err)
	}
	if action == "get_photo" {
		if h.deps.User == nil {
			return parityError("profile service unavailable")
		}
		value, err := h.deps.User.Avatar(ctx, user.AvatarRequest{Phone: id})
		return parityResult(value, err)
	}
	if h.deps.Group == nil {
		return parityError("group service unavailable")
	}
	switch action {
	case "set_photo":
		if _, err = requiredText(r, "media_id"); err != nil {
			return parityResult(nil, err)
		}
		photo, cleanup, err := stagedUpload(ctx, r, h.deps.Data, "image")
		if err != nil {
			return parityResult(nil, err)
		}
		defer cleanup()
		picture, err := h.deps.Group.SetGroupPhoto(ctx, group.SetGroupPhotoRequest{GroupID: id, Photo: photo})
		return parityResult(map[string]any{"picture_id": picture}, err)
	case "remove_photo":
		picture, err := h.deps.Group.SetGroupPhoto(ctx, group.SetGroupPhotoRequest{GroupID: id})
		return parityResult(map[string]any{"picture_id": picture, "removed": err == nil}, err)
	default:
		if h.deps.Data == nil {
			return parityError("private export store unavailable")
		}
		participants, err := h.deps.Group.GetGroupParticipants(ctx, group.GetGroupParticipantsRequest{GroupID: id})
		if err != nil {
			return parityResult(nil, err)
		}
		page, err := pageResult(participants.Participants, r.GetInt("limit", 500), r.GetInt("offset", 0))
		if err != nil {
			return parityResult(nil, err)
		}
		page["group_id"] = participants.GroupID
		page["group_name"] = participants.Name
		return h.exportJSON(ctx, "participants.json", page, map[string]any{"has_more": page["has_more"], "next_offset": page["next_offset"], "total": page["total"]}, r.GetBool("inline", false))
	}
}
func (h *parityHandler) profile(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	ctx, device, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	if h.deps.User == nil {
		return parityError("profile service unavailable")
	}
	phone := r.GetString("phone", "")
	if phone == "" {
		phone = device.JID()
	}
	action := r.GetString("action", "")
	if action == "update_profile" {
		name, media := r.GetString("push_name", ""), r.GetString("media_id", "")
		if (name == "") == (media == "") {
			return parityError("update_profile accepts exactly one of push_name or media_id")
		}
		if name != "" {
			action = "update_push_name"
		} else {
			action = "update_avatar"
		}
	}
	switch action {
	case "get_profile":
		value, err := h.deps.User.Info(ctx, user.InfoRequest{Phone: phone})
		return parityResult(value, err)
	case "get_avatar":
		value, err := h.deps.User.Avatar(ctx, user.AvatarRequest{Phone: phone, IsPreview: r.GetBool("is_preview", false), IsCommunity: r.GetBool("is_community", false)})
		return parityResult(value, err)
	case "get_privacy":
		value, err := h.deps.User.MyPrivacySetting(ctx)
		return parityResult(value, err)
	case "get_business_profile":
		value, err := h.deps.User.BusinessProfile(ctx, user.BusinessProfileRequest{Phone: phone})
		return parityResult(value, err)
	case "check_number":
		phone, err := requiredText(r, "phone")
		if err != nil {
			return parityResult(nil, err)
		}
		value, err := h.deps.User.IsOnWhatsApp(ctx, user.CheckRequest{Phone: phone})
		return parityResult(value, err)
	case "update_push_name":
		name, err := requiredText(r, "push_name")
		if err != nil {
			return parityResult(nil, err)
		}
		return parityOK(h.deps.User.ChangePushName(ctx, user.ChangePushNameRequest{PushName: name}))
	case "update_avatar":
		if _, err = requiredText(r, "media_id"); err != nil {
			return parityResult(nil, err)
		}
		photo, cleanup, err := stagedUpload(ctx, r, h.deps.Data, "image")
		if err != nil {
			return parityResult(nil, err)
		}
		defer cleanup()
		return parityOK(h.deps.User.ChangeAvatar(ctx, user.ChangeAvatarRequest{Avatar: photo}))
	default:
		return parityError("unknown or unsupported profile action")
	}
}
func (h *parityHandler) newsletter(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	action := r.GetString("action", "")
	if action == "list" {
		if h.deps.User == nil {
			return parityError("profile service unavailable")
		}
		value, err := h.deps.User.MyListNewsletter(ctx)
		if err != nil {
			return parityResult(nil, err)
		}
		page, err := pageResult(value.Data, r.GetInt("limit", 50), r.GetInt("offset", 0))
		return parityResult(page, err)
	}
	if h.deps.Newsletter == nil {
		return parityError("newsletter service unavailable")
	}
	id, err := requiredText(r, "newsletter_id")
	if err != nil {
		return parityResult(nil, err)
	}
	switch action {
	case "get_messages":
		count, before := r.GetInt("count", 50), r.GetInt("before", 0)
		if count < 1 || count > 100 || before < 0 || before > 2147483646 {
			return parityError("invalid newsletter pagination")
		}
		value, err := h.deps.Newsletter.GetMessages(ctx, newsletter.GetMessagesRequest{NewsletterID: id, Count: count, Before: before})
		if err != nil {
			return parityResult(nil, err)
		}
		next := 0
		for _, m := range value.Data {
			if next == 0 || m.ServerID < next {
				next = m.ServerID
			}
		}
		return parityResult(map[string]any{"data": value.Data, "next_before": next, "requested_count": count, "may_have_more": len(value.Data) == count}, nil)
	case "unfollow":
		return parityOK(h.deps.Newsletter.Unfollow(ctx, newsletter.UnfollowRequest{NewsletterID: id}))
	case "download_media":
		if h.deps.Data == nil {
			return parityError("private media store unavailable")
		}
		serverID := r.GetInt("server_id", 0)
		if serverID < 1 || serverID > 2147483646 {
			return parityError("server_id must be 1..2147483646")
		}
		if _, err = dataDevice(ctx); err != nil {
			return parityResult(nil, err)
		}
		value, err := h.deps.Newsletter.DownloadMedia(ctx, newsletter.DownloadMediaRequest{NewsletterID: id, ServerID: serverID, MCPPrivate: true})
		if err != nil {
			return parityResult(nil, err)
		}
		importer := &MessageHandler{data: h.deps.Data}
		return importer.importDownload(ctx, r, message.DownloadMediaResponse{MessageID: value.MessageID, MediaType: value.MediaType, Filename: value.Filename, FilePath: value.FilePath, FileSize: value.FileSize})
	default:
		return parityError("unknown or unsupported newsletter action")
	}
}
func (h *parityHandler) history(ctx context.Context, r mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	ctx, _, err := resolveDeviceContext(ctx, r, h.resolver)
	if err != nil {
		return parityResult(nil, err)
	}
	service, ok := h.deps.Chat.(chat.IHistoryUsecase)
	if !ok {
		return parityError("history service unavailable")
	}
	req := chat.HistoryRequest{Action: r.GetString("action", ""), ChatJID: r.GetString("chat_jid", ""), MessageID: r.GetString("message_id", ""), Sender: r.GetString("sender", ""), Search: r.GetString("search", ""), StartTime: r.GetString("start_time", ""), EndTime: r.GetString("end_time", ""), MediaOnly: r.GetBool("media_only", false), MediaType: r.GetString("media_type", ""), MessageType: r.GetString("message_type", ""), Limit: r.GetInt("limit", 50), Offset: r.GetInt("offset", 0), Before: r.GetInt("before", 10), After: r.GetInt("after", 10), Count: r.GetInt("count", 50)}
	if _, exists := r.GetArguments()["is_from_me"]; exists {
		value := r.GetBool("is_from_me", false)
		req.IsFromMe = &value
	}
	if req.Action == "export" && h.deps.Data == nil {
		return parityError("private export store unavailable")
	}
	value, err := service.History(ctx, req)
	if err != nil {
		return parityResult(nil, err)
	}
	if req.Action != "export" {
		return parityResult(value, nil)
	}
	return h.exportJSON(ctx, "history.json", value, map[string]any{"limit": value.Limit, "offset": value.Offset, "total": value.Total, "next_offset": value.NextOffset, "has_more": value.HasMore, "completeness": "unknown"}, r.GetBool("inline", false))
}
func (h *parityHandler) exportJSON(ctx context.Context, name string, value any, metadata map[string]any, inline bool) (*mcpg.CallToolResult, error) {
	if h.deps.Data == nil {
		return parityError("private export store unavailable")
	}
	device, err := dataDevice(ctx)
	if err != nil {
		return parityResult(nil, err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return parityResult(nil, err)
	}
	if len(raw) > mcpstore.MaxMediaBytes {
		return parityError("export exceeds 10 MiB; reduce limit or narrow filters")
	}
	media, err := h.deps.Data.Stage(ctx, device, name, "application/json", raw)
	if err != nil {
		return parityResult(nil, err)
	}
	metadata["media"] = media
	result := mcpg.NewToolResultStructuredOnly(metadata)
	if inline {
		result.Content = append(result.Content, mcpg.EmbeddedResource{Type: "resource", Resource: mediaBlob(media, raw)})
	}
	return result, nil
}
