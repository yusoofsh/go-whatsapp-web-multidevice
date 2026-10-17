package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

// validatingChatService runs the production pagination validators, the same
// way usecase.serviceChat does, so the REST handler sees the real error type.
type validatingChatService struct {
	requestChatHistorySpy
	listCalls     int
	messagesCalls int
}

func (s *validatingChatService) ListChats(ctx context.Context, request domainChat.ListChatsRequest) (domainChat.ListChatsResponse, error) {
	if err := validations.ValidateListChats(ctx, &request); err != nil {
		return domainChat.ListChatsResponse{}, err
	}
	s.listCalls++
	return domainChat.ListChatsResponse{}, nil
}

func (s *validatingChatService) GetChatMessages(ctx context.Context, request domainChat.GetChatMessagesRequest) (domainChat.GetChatMessagesResponse, error) {
	if err := validations.ValidateGetChatMessages(ctx, &request); err != nil {
		return domainChat.GetChatMessagesResponse{}, err
	}
	s.messagesCalls++
	return domainChat.GetChatMessagesResponse{}, nil
}

// TestChatListLimitValidationIsClean400 pins that an out-of-range limit is a
// plain 400 VALIDATION_ERROR envelope rather than a panic caught (and logged
// as "Panic recovered") by the Recovery middleware.
func TestChatListLimitValidationIsClean400(t *testing.T) {
	hook := logrustest.NewGlobal()
	t.Cleanup(func() { logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks)) })

	svc := &validatingChatService{}
	controller := &Chat{Service: svc}
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Get("/chats", controller.ListChats)
	app.Get("/chat/:chat_jid/messages", controller.GetChatMessages)

	tests := []struct {
		name    string
		path    string
		status  int
		message string
	}{
		{"chats limit too large", "/chats?limit=1000", fiber.StatusBadRequest, "limit: must be no greater than 100."},
		{"chats negative limit", "/chats?limit=-1", fiber.StatusBadRequest, "limit: must be no less than 1."},
		{"chats negative offset", "/chats?offset=-1", fiber.StatusBadRequest, "offset: must be no less than 0."},
		{"chats max limit ok", "/chats?limit=100", fiber.StatusOK, ""},
		{"messages limit too large", "/chat/628123%40s.whatsapp.net/messages?limit=1000", fiber.StatusBadRequest, "limit: must be no greater than 100."},
		{"messages default ok", "/chat/628123%40s.whatsapp.net/messages", fiber.StatusOK, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			resp, err := app.Test(httptest.NewRequest("GET", tt.path, nil))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tt.status, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			var envelope utils.ResponseData
			require.NoError(t, json.Unmarshal(body, &envelope), "body must be the JSON envelope, got: %s", body)
			if tt.status == fiber.StatusBadRequest {
				require.Equal(t, "VALIDATION_ERROR", envelope.Code)
				require.Equal(t, tt.message, envelope.Message)
			} else {
				require.Equal(t, "SUCCESS", envelope.Code)
			}

			for _, entry := range hook.AllEntries() {
				require.NotContains(t, entry.Message, "Panic recovered", "validation errors must not go through the panic path")
			}
		})
	}
	require.Equal(t, 1, svc.listCalls)
	require.Equal(t, 1, svc.messagesCalls)
}
