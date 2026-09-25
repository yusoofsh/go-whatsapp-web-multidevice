package mcp

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"
	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	domainGroup "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/group"
	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	domainSend "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/mark3labs/mcp-go/server"
)

// Deps carries the usecase instances the MCP tools call — the same instances
// the REST handlers hold, so both surfaces share one whatsmeow session.
type Deps struct {
	Data    *mcpstore.Store
	App     domainApp.IAppUsecase
	Send    domainSend.ISendUsecase
	Chat    domainChat.IChatUsecase
	User    domainUser.IUserUsecase
	Message domainMessage.IMessageUsecase
	Group   domainGroup.IGroupUsecase
}

// NewServer registers the five upstream tools and optional media/event tools.
func NewServer(deps Deps, resolver deviceResolver, options ...server.ServerOption) *server.MCPServer {
	opts := []server.ServerOption{
		server.WithToolCapabilities(true),
		// Enforce the schemas' allOf/if/then conditionals at the mcp-go
		// layer, before any handler runs (SEP-1303). Without this,
		// inputValidator stays nil and the conditionals are advisory only.
		server.WithInputSchemaValidation(),
	}
	if deps.Data != nil {
		opts = append(opts, server.WithResourceCapabilities(false, false))
	}
	opts = append(opts, options...)
	s := server.NewMCPServer("WhatsApp Web Multidevice MCP Server", config.AppVersion, opts...)
	InitMcpSend(deps.Send, resolver, deps.Data).AddSendTools(s)
	InitMcpMessage(deps.Message, resolver, deps.Data).AddMessageTools(s)
	InitMcpChat(deps.Chat, deps.User, resolver).AddChatTools(s)
	InitMcpGroup(deps.Group, resolver).AddGroupTools(s)
	InitMcpApp(deps.App, resolver).AddAppTools(s)
	if deps.Data != nil {
		registerDataTools(s, deps.Data, resolver)
	}
	return s
}
