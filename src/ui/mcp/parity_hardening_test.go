package mcp

import (
	"context"
	"strings"
	"testing"

	send "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/send"
	mcpg "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func assertHardeningRejected(t *testing.T, result map[string]any) {
	t.Helper()
	if result["error"] == nil {
		body, ok := result["result"].(map[string]any)
		require.True(t, ok, result)
		require.Equal(t, true, body["isError"], result)
	}
}

func TestPresenceSchedulingRejectedBeforeDispatch(t *testing.T) {
	fields := []struct {
		name string
		value any
	}{
		{"scheduled_at", "2099-01-01T00:00:00Z"},
		{"timezone", "Asia/Jakarta"},
		{"recurrence", "daily"},
		{"weekdays", []int{1}},
		{"day_of_month", 1},
		{"end_at", "2099-02-01T00:00:00Z"},
		{"occurrence_limit", 2},
		{"scheduled_at", ""},
	}
	for _, kind := range []string{"presence", "chat_presence"} {
		for _, field := range fields {
			t.Run(kind+"/"+field.name, func(t *testing.T) {
				s := newParityTestSuite(t)
				args := map[string]any{"type": kind, field.name: field.value}
				if kind == "presence" {
					args["presence"] = "available"
				} else {
					args["chat_presence"] = "start"
					args["phone"] = "222"
				}
				raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": "whatsapp_send", "arguments": args})
				assertHardeningRejected(t, raw)
				require.Empty(t, s.send.kind, "schema rejection must precede dispatch")
				var request mcpg.CallToolRequest
				request.Params.Arguments = args
				h := &parityHandler{deps: Deps{Send: s.send}}
				result, err := h.send(s.ctx, request)
				require.NoError(t, err)
				require.True(t, result.IsError)
				require.Empty(t, s.send.kind, "direct handler must also reject before dispatch")
			})
		}
	}
}

type hardeningScheduleSpy struct {
	paritySendSpy
	options send.ScheduleOptions
}

func (s *hardeningScheduleSpy) SendText(ctx context.Context, request send.MessageRequest) (send.GenericResponse, error) {
	s.options = request.ScheduleOptions
	return s.record(ctx, "text", request.Phone)
}

func TestTextAndStatusSchedulingArePreserved(t *testing.T) {
	for _, kind := range []string{"text", "status"} {
		t.Run(kind, func(t *testing.T) {
			s := newParityTestSuite(t)
			spy := &hardeningScheduleSpy{}
			server := NewServer(Deps{Send: spy}, nil)
			args := map[string]any{"type": kind, "message": "fixture only", "scheduled_at": "2099-01-01T00:00:00Z", "timezone": "Asia/Jakarta", "recurrence": "daily", "occurrence_limit": 2}
			if kind == "status" {
				args["status_type"] = "text"
			} else {
				args["phone"] = "222"
			}
			raw := invokeData(t, server, s.ctx, "tools/call", map[string]any{"name": "whatsapp_send", "arguments": args})
			require.Nil(t, raw["error"], raw)
			require.NotEqual(t, true, raw["result"].(map[string]any)["isError"], raw)
			require.Equal(t, "2099-01-01T00:00:00Z", spy.options.ScheduledAt)
			require.Equal(t, "Asia/Jakarta", spy.options.Timezone)
			require.Equal(t, "daily", spy.options.Recurrence)
			require.Equal(t, 2, spy.options.OccurrenceLimit)
			if kind == "status" {
				require.Equal(t, "status@broadcast", spy.phone)
			}
		})
	}
}

func TestProfileMutationTargetRejectedBeforeDispatch(t *testing.T) {
	for _, action := range []string{"update_profile", "update_push_name", "update_avatar"} {
		t.Run(action, func(t *testing.T) {
			s := newParityTestSuite(t)
			args := map[string]any{"action": action, "phone": "222@s.whatsapp.net"}
			if action == "update_avatar" {
				args["media_id"] = "00000000-0000-0000-0000-000000000000"
			} else {
				args["push_name"] = "must not be applied"
			}
			raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": "whatsapp_profile", "arguments": args})
			assertHardeningRejected(t, raw)
			var request mcpg.CallToolRequest
			request.Params.Arguments = args
			h := &parityHandler{deps: Deps{User: s.user}}
			result, err := h.profile(s.ctx, request)
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Empty(t, s.user.pushName)
			require.False(t, s.user.avatar)
		})
	}
	s := newParityTestSuite(t)
	s.invoke(t, "whatsapp_profile", map[string]any{"action": "get_profile", "phone": "222@s.whatsapp.net"})
	require.Equal(t, "222@s.whatsapp.net", s.user.phone)
	s.invoke(t, "whatsapp_profile", map[string]any{"action": "update_push_name", "push_name": "Fixture", "device_id": "b"})
	require.Equal(t, "Fixture", s.user.pushName)
}

func TestNewGroupOperationsNormalizeTargets(t *testing.T) {
	for _, id := range []string{"10", "10-20", "10@g.us", " 10 "} {
		t.Run(id, func(t *testing.T) {
			s := newParityTestSuite(t)
			want := strings.TrimSpace(id)
			if !strings.Contains(want, "@") {
				want += "@g.us"
			}
			s.invoke(t, "whatsapp_group", map[string]any{"action": "get_photo", "group_id": id})
			require.Equal(t, want, s.user.phone)
		})
	}
	for _, id := range []string{"", "@g.us", "222@s.whatsapp.net", "1@newsletter", "a@g.us", "-10", "10-", "1-2-3", strings.Repeat("1", 253)} {
		_, err := normalizeParityGroupID(id)
		require.Error(t, err, id)
	}
	for _, action := range []string{"get_photo", "set_photo", "remove_photo", "export_participants"} {
		s := newParityTestSuite(t)
		args := map[string]any{"action": action, "group_id": "222@s.whatsapp.net", "media_id": "00000000-0000-0000-0000-000000000000"}
		raw := invokeData(t, s.s, s.ctx, "tools/call", map[string]any{"name": "whatsapp_group", "arguments": args})
		assertHardeningRejected(t, raw)
		require.Empty(t, s.user.phone)
		require.False(t, s.group.photo)
		require.False(t, s.group.removed)
	}
}
