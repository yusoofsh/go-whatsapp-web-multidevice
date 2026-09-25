package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	mcpg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type ChatHandler struct {
	chatService domainChat.IChatUsecase
	userService domainUser.IUserUsecase
	resolver    deviceResolver
}

func InitMcpChat(chatService domainChat.IChatUsecase, userService domainUser.IUserUsecase, resolver deviceResolver) *ChatHandler {
	return &ChatHandler{chatService: chatService, userService: userService, resolver: resolver}
}

func (h *ChatHandler) AddChatTools(mcpServer *server.MCPServer) {
	tool := mcpg.NewTool("whatsapp_chat",
		mcpg.WithDescription("Query WhatsApp chats and contacts: list_chats, list_contacts, get_messages (chat history with filters), request_history (older messages from the phone), or archive/unarchive a chat."),
		mcpg.WithTitleAnnotation("Chat Queries"),
		mcpg.WithReadOnlyHintAnnotation(false),
		mcpg.WithDestructiveHintAnnotation(false),
		mcpg.WithIdempotentHintAnnotation(true),
		mcpg.WithRawInputSchema(json.RawMessage(chatSchema)),
	)
	// NewTool defaults InputSchema.Type to "object"; clear it so only
	// RawInputSchema is set, or MarshalJSON rejects the tool as conflicting.
	tool.InputSchema = mcpg.ToolInputSchema{}
	mcpServer.AddTool(tool, h.handleChat)
}

func (h *ChatHandler) handleChat(ctx context.Context, request mcpg.CallToolRequest) (*mcpg.CallToolResult, error) {
	ctx, _, err := resolveDeviceContext(ctx, request, h.resolver)
	if err != nil {
		return mcpg.NewToolResultError(err.Error()), nil
	}

	action, err := request.RequireString("action")
	if err != nil {
		return mcpg.NewToolResultError(err.Error()), nil
	}

	switch action {
	case "list_chats":
		req := domainChat.ListChatsRequest{
			Limit:    request.GetInt("limit", 25),
			Offset:   request.GetInt("offset", 0),
			Search:   request.GetString("search", ""),
			HasMedia: request.GetBool("has_media", false),
		}
		resp, err := h.chatService.ListChats(ctx, req)
		if err != nil {
			return mcpg.NewToolResultError(err.Error()), nil
		}
		return mcpg.NewToolResultStructured(resp, fmt.Sprintf("Retrieved %d chats (offset %d, limit %d)", len(resp.Data), req.Offset, req.Limit)), nil
	case "list_contacts":
		resp, err := h.userService.MyListContacts(ctx)
		if err != nil {
			return mcpg.NewToolResultError(err.Error()), nil
		}
		return mcpg.NewToolResultStructured(resp, fmt.Sprintf("Found %d contacts", len(resp.Data))), nil
	case "get_messages":
		chatJID := request.GetString("chat_jid", "")
		var startTimePtr, endTimePtr *string
		if v := strings.TrimSpace(request.GetString("start_time", "")); v != "" {
			startTimePtr = &v
		}
		if v := strings.TrimSpace(request.GetString("end_time", "")); v != "" {
			endTimePtr = &v
		}
		var isFromMePtr *bool
		if args := request.GetArguments(); args != nil {
			if _, ok := args["is_from_me"]; ok {
				v := request.GetBool("is_from_me", false)
				isFromMePtr = &v
			}
		}
		req := domainChat.GetChatMessagesRequest{
			ChatJID:   chatJID,
			Limit:     request.GetInt("limit", 50),
			Offset:    request.GetInt("offset", 0),
			StartTime: startTimePtr,
			EndTime:   endTimePtr,
			MediaOnly: request.GetBool("media_only", false),
			IsFromMe:  isFromMePtr,
			Search:    request.GetString("search", ""),
		}
		resp, err := h.chatService.GetChatMessages(ctx, req)
		if err != nil {
			return mcpg.NewToolResultError(err.Error()), nil
		}
		return mcpg.NewToolResultStructured(resp, fmt.Sprintf("Retrieved %d messages from %s", len(resp.Data), chatJID)), nil
	case "request_history":
		count := request.GetInt("count", 50)
		if count < 1 || count > 500 {
			return mcpg.NewToolResultError("count must be 1..500"), nil
		}
		resp, err := h.chatService.RequestChatHistory(ctx, domainChat.RequestChatHistoryRequest{
			ChatJID: request.GetString("chat_jid", ""), Count: count,
		})
		if err != nil {
			return mcpg.NewToolResultError(err.Error()), nil
		}
		return mcpg.NewToolResultStructured(resp, "History requested; query get_messages after history.sync. WhatsApp decides what is available."), nil
	case "archive":
		req := domainChat.ArchiveChatRequest{
			ChatJID:  request.GetString("chat_jid", ""),
			Archived: request.GetBool("archived", false),
		}
		resp, err := h.chatService.ArchiveChat(ctx, req)
		if err != nil {
			return mcpg.NewToolResultError(err.Error()), nil
		}
		return mcpg.NewToolResultStructured(resp, resp.Message), nil
	default:
		return mcpg.NewToolResultError(fmt.Sprintf("unknown chat action: %s", action)), nil
	}
}
