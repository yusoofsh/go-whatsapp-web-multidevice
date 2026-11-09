package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/require"
)

type historyServiceSpy struct {
	stubChatService
	calls     []chat.HistoryRequest
	responses map[string]chat.HistoryResponse
	errs      map[string]error
}

func (s *historyServiceSpy) History(_ context.Context, request chat.HistoryRequest) (chat.HistoryResponse, error) {
	s.calls = append(s.calls, request)
	return s.responses[request.Action], s.errs[request.Action]
}

func historyCallContext() context.Context {
	return whatsapp.ContextWithDevice(context.Background(), pairedTestDevice("a", "628111000111"))
}

func callHistory(t *testing.T, h *parityHandler, args map[string]any) chat.HistoryResponse {
	t.Helper()
	result, err := h.history(historyCallContext(), callReq(args))
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	value, ok := result.StructuredContent.(chat.HistoryResponse)
	require.True(t, ok, "%T", result.StructuredContent)
	return value
}

func TestParityHistoryAddsCoverageForChat(t *testing.T) {
	t.Setenv(phoneVerificationFileEnv, "")
	oldest := time.Date(2021, time.May, 4, 0, 0, 0, 0, time.UTC)
	coverage := &chatstorage.ArchiveCoverage{ChatJID: verificationChat, MessageCount: 4, OldestTimestamp: &oldest}
	spy := &historyServiceSpy{responses: map[string]chat.HistoryResponse{
		"query":    {Action: "query", Completeness: "unknown"},
		"coverage": {Action: "coverage", Coverage: coverage},
	}}
	h := &parityHandler{deps: Deps{Chat: spy}}

	value := callHistory(t, h, map[string]any{"action": "query", "chat_jid": verificationChat})

	require.Len(t, spy.calls, 2)
	require.Equal(t, chat.HistoryRequest{Action: "coverage", ChatJID: verificationChat}, spy.calls[1])
	require.Same(t, coverage, value.Coverage)
	require.Equal(t, "query", value.Action)
}

func TestParityHistorySkipsCoverageLookup(t *testing.T) {
	t.Setenv(phoneVerificationFileEnv, "")
	existing := &chatstorage.ArchiveCoverage{ChatJID: verificationChat}
	for name, tc := range map[string]struct {
		args     map[string]any
		response chat.HistoryResponse
	}{
		"no chat":               {map[string]any{"action": "query"}, chat.HistoryResponse{}},
		"coverage already here": {map[string]any{"action": "query", "chat_jid": verificationChat}, chat.HistoryResponse{Coverage: existing}},
	} {
		t.Run(name, func(t *testing.T) {
			spy := &historyServiceSpy{responses: map[string]chat.HistoryResponse{"query": tc.response}}
			callHistory(t, &parityHandler{deps: Deps{Chat: spy}}, tc.args)
			require.Len(t, spy.calls, 1)
		})
	}
}

func TestParityHistoryAppliesPhoneVerification(t *testing.T) {
	writeVerificationManifest(t, verificationManifest("matches_oldest_visible_date", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
	oldest := time.Date(2021, time.May, 4, 9, 0, 0, 0, time.UTC)
	spy := &historyServiceSpy{responses: map[string]chat.HistoryResponse{
		"query":    {Action: "query", Completeness: "unknown"},
		"coverage": {Coverage: &chatstorage.ArchiveCoverage{ChatJID: verificationChat, MessageCount: 4, OldestTimestamp: &oldest}},
	}}

	value := callHistory(t, &parityHandler{deps: Deps{Chat: spy}}, map[string]any{"action": "query", "chat_jid": verificationChat})

	require.Equal(t, "oldest_date_verified", value.Completeness)
	require.NotNil(t, value.PhoneVerification)
}

// TestParityHistoryUnusableCoverageSkipsVerification pins that a phone check
// is never attached when the local coverage it relies on could not be read.
func TestParityHistoryUnusableCoverageSkipsVerification(t *testing.T) {
	writeVerificationManifest(t, verificationManifest("not_available_on_phone", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
	for name, spy := range map[string]*historyServiceSpy{
		"coverage error": {
			responses: map[string]chat.HistoryResponse{"query": {Completeness: "unknown"}},
			errs:      map[string]error{"coverage": errors.New("storage offline")},
		},
		"coverage missing": {
			responses: map[string]chat.HistoryResponse{"query": {Completeness: "unknown"}, "coverage": {}},
		},
		"coverage for another chat": {
			responses: map[string]chat.HistoryResponse{"query": {Completeness: "unknown"}, "coverage": {Coverage: &chatstorage.ArchiveCoverage{ChatJID: "628333000333@s.whatsapp.net"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := callHistory(t, &parityHandler{deps: Deps{Chat: spy}}, map[string]any{"action": "query", "chat_jid": verificationChat})
			require.Nil(t, value.PhoneVerification)
			require.Nil(t, value.Coverage)
			require.Equal(t, "unknown", value.Completeness)
		})
	}
}

func TestParityHistoryExportReportsCompleteness(t *testing.T) {
	writeVerificationManifest(t, verificationManifest("not_available_on_phone", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
	spy := &historyServiceSpy{responses: map[string]chat.HistoryResponse{
		"export":   {Action: "export", Completeness: "unknown"},
		"coverage": {Coverage: &chatstorage.ArchiveCoverage{ChatJID: verificationChat}},
	}}
	h := &parityHandler{deps: Deps{Chat: spy, Data: parityStore(t)}}

	result, err := h.history(historyCallContext(), callReq(map[string]any{"action": "export", "chat_jid": verificationChat}))

	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	metadata, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok, "%T", result.StructuredContent)
	require.Equal(t, "unverified", metadata["completeness"])
}
