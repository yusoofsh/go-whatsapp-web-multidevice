package whatsapp

import (
	"context"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func fullHistorySyncFixture(chatJID, messageID string, timestamp time.Time) *waHistorySync.HistorySync {
	syncType := waHistorySync.HistorySync_FULL
	unix := uint64(timestamp.Unix())
	return &waHistorySync.HistorySync{
		SyncType: &syncType,
		Conversations: []*waHistorySync.Conversation{{
			ID:                  proto.String(chatJID),
			EphemeralExpiration: proto.Uint32(0),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chatJID),
					FromMe:    proto.Bool(false),
					ID:        proto.String(messageID),
				},
				Message:          &waE2E.Message{Conversation: proto.String("an old message")},
				MessageTimestamp: &unix,
			}}},
		}},
	}
}

func TestProcessHistorySyncPersistsFullSync(t *testing.T) {
	originalLog := log
	log = waLog.Noop
	defer func() { log = originalLog }()

	deviceID := "device-a@s.whatsapp.net"
	chatJID := "628123456789@s.whatsapp.net"
	repo := &historyMessageBatchRepoSpy{}
	ctx := ContextWithDevice(context.Background(), NewDeviceInstance(deviceID, nil, nil))
	data := fullHistorySyncFixture(chatJID, "full-msg-1", time.Date(2025, time.March, 1, 8, 0, 0, 0, time.UTC))

	require.NoError(t, processHistorySync(ctx, data, repo, nil))

	require.Equal(t, 1, repo.storeMessagesBatchCalls, "FULL conversations must be stored, not skipped")
	require.Len(t, repo.lastBatch, 1)
	require.Equal(t, "full-msg-1", repo.lastBatch[0].ID)
}

func TestProcessConversationMessagesFullSyncPreservesChatMetadata(t *testing.T) {
	originalLog := log
	log = waLog.Noop
	defer func() { log = originalLog }()

	deviceID := "device-a@s.whatsapp.net"
	chatJID := "628123456789@s.whatsapp.net"
	repo := &historyMessageBatchRepoSpy{
		existingChat: &domainChatStorage.Chat{
			DeviceID:            deviceID,
			JID:                 chatJID,
			LastMessageTime:     time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC),
			Archived:            true,
			EphemeralExpiration: 604800,
		},
	}
	ctx := ContextWithDevice(context.Background(), NewDeviceInstance(deviceID, nil, nil))
	data := fullHistorySyncFixture(chatJID, "full-msg-2", time.Date(2025, time.March, 1, 8, 0, 0, 0, time.UTC))

	require.NoError(t, processConversationMessages(ctx, data, repo, nil))

	require.NotNil(t, repo.lastStoredChat)
	require.True(t, repo.lastStoredChat.Archived, "a FULL sync must not unarchive the chat")
	require.False(t, repo.lastStoredChat.EphemeralExpirationSet(), "a FULL sync must not set the disappearing timer")
}
