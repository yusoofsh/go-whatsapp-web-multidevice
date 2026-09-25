package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	chat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	storage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type archiveUsecaseSpy struct {
	storage.IChatStorageRepository
	filter   storage.ArchiveFilter
	messages []*storage.Message
	err      error
}

func (s *archiveUsecaseSpy) QueryArchive(_ context.Context, f storage.ArchiveFilter) ([]*storage.Message, int64, error) {
	s.filter = f
	return s.messages, 7, s.err
}
func (s *archiveUsecaseSpy) ContextArchive(_ context.Context, device, chat, id string, before, after int) ([]*storage.Message, *storage.Message, []*storage.Message, error) {
	return nil, nil, nil, storage.ErrArchiveAnchorNotFound
}
func (s *archiveUsecaseSpy) CoverageArchive(_ context.Context, device, chat string) (storage.ArchiveCoverage, error) {
	return storage.ArchiveCoverage{ChatJID: chat, MessageCount: 7}, s.err
}
func (s *archiveUsecaseSpy) GetChatByDevice(device, jid string) (*storage.Chat, error) {
	return &storage.Chat{DeviceID: device, JID: jid, Name: "Fixture"}, nil
}
func (s *archiveUsecaseSpy) GetChatMessageCountByDevice(device, jid string) (int64, error) {
	return 999, nil
}
func historyFixtureContext() context.Context {
	jid := types.NewJID("111", types.DefaultUserServer)
	return whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("alias", &whatsmeow.Client{Store: &store.Device{ID: &jid}}, nil))
}
func TestHistoryUsecaseScopesAndRedactsArchive(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	repo := &archiveUsecaseSpy{messages: []*storage.Message{{ID: "m1", ChatJID: "10@g.us", Sender: "222@s.whatsapp.net", Content: "invoice", Timestamp: now, MediaKey: []byte("secret-key"), URL: "secret-CDN-token", DirectPath: "secret-path"}}}
	service := NewChatService(repo).(chat.IHistoryUsecase)
	sent := false
	response, err := service.History(historyFixtureContext(), chat.HistoryRequest{Action: "search_all", Sender: "222@s.whatsapp.net", Search: "invoice", StartTime: "2026-09-01", EndTime: "2026-09-02T00:00:00Z", IsFromMe: &sent, Limit: 2, Offset: 3})
	require.NoError(t, err)
	require.Equal(t, "111@s.whatsapp.net", repo.filter.DeviceID)
	require.Equal(t, "222@s.whatsapp.net", repo.filter.Sender)
	require.NotNil(t, repo.filter.IsFromMe)
	require.False(t, *repo.filter.IsFromMe)
	require.NotNil(t, repo.filter.StartTime)
	require.Equal(t, 4, response.NextOffset)
	require.True(t, response.HasMore)
	require.Equal(t, "unknown", response.Completeness)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
	require.NotContains(t, string(raw), "media_key")
	_, err = service.History(historyFixtureContext(), chat.HistoryRequest{Action: "export", Limit: 1})
	require.NoError(t, err)
	require.True(t, repo.filter.Asc)
	response, err = service.History(historyFixtureContext(), chat.HistoryRequest{Action: "coverage", ChatJID: "10@g.us"})
	require.NoError(t, err)
	require.Equal(t, int64(7), response.Coverage.MessageCount)
}
func TestHistoryUsecaseErrorsWithoutNetwork(t *testing.T) {
	spy := &archiveUsecaseSpy{}
	service := NewChatService(spy).(chat.IHistoryUsecase)
	_, err := service.History(context.Background(), chat.HistoryRequest{Action: "search_all", Limit: 10})
	require.Error(t, err)
	for _, request := range []chat.HistoryRequest{{Action: "search_all", Limit: 501}, {Action: "search_all", Limit: 10, StartTime: "invalid"}, {Action: "search_all", Limit: 10, StartTime: "2026-10-01", EndTime: "2026-09-01"}, {Action: "request_backfill", Count: 501}, {Action: "not-an-action"}, {Action: "context", ChatJID: "10@g.us", MessageID: "missing", Before: 1, After: 1}} {
		_, err = service.History(historyFixtureContext(), request)
		require.Error(t, err)
	}
	spy.err = errors.New("fixture storage failure")
	_, err = service.History(historyFixtureContext(), chat.HistoryRequest{Action: "search_all", Limit: 10})
	require.ErrorContains(t, err, "fixture storage failure")
	service = NewChatService(nil).(chat.IHistoryUsecase)
	_, err = service.History(historyFixtureContext(), chat.HistoryRequest{Action: "search_all", Limit: 10})
	require.ErrorContains(t, err, "disabled")
}
func TestPerChatSearchKeepsAllFiltersAndPagination(t *testing.T) {
	repo := &archiveUsecaseSpy{messages: []*storage.Message{}}
	service := NewChatService(repo)
	start, end := "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z"
	sent := false
	response, err := service.GetChatMessages(historyFixtureContext(), chat.GetChatMessagesRequest{ChatJID: "10@g.us", Search: "invoice", Limit: 5, Offset: 2, StartTime: &start, EndTime: &end, MediaOnly: true, IsFromMe: &sent})
	require.NoError(t, err)
	require.Equal(t, "111@s.whatsapp.net", repo.filter.DeviceID)
	require.Equal(t, "10@g.us", repo.filter.ChatJID)
	require.Equal(t, 2, repo.filter.Offset)
	require.True(t, repo.filter.MediaOnly)
	require.NotNil(t, repo.filter.StartTime)
	require.NotNil(t, repo.filter.EndTime)
	require.NotNil(t, repo.filter.IsFromMe)
	require.False(t, *repo.filter.IsFromMe)
	require.Equal(t, 7, response.Pagination.Total)
}
