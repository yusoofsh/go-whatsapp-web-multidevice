package chatstorage

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	timerDeviceID = "15550101000@s.whatsapp.net"
	timerChatJID  = "15550102000@s.whatsapp.net"
	weekTimer     = uint32(604800)
)

func storeChatWithTimer(t *testing.T, repo *SQLiteRepository, jid, name string, lastMessageTime time.Time, seconds uint32) {
	t.Helper()
	chat := &domainChatStorage.Chat{DeviceID: timerDeviceID, JID: jid, Name: name, LastMessageTime: lastMessageTime}
	chat.SetEphemeralExpiration(seconds)
	if err := repo.StoreChat(chat); err != nil {
		t.Fatalf("store chat with timer %d: %v", seconds, err)
	}
}

func storedChat(t *testing.T, repo *SQLiteRepository, jid string) *domainChatStorage.Chat {
	t.Helper()
	chat, err := repo.GetChatByDevice(timerDeviceID, jid)
	if err != nil {
		t.Fatalf("get chat %s: %v", jid, err)
	}
	if chat == nil {
		t.Fatalf("chat %s was not stored", jid)
	}
	return chat
}

// Replays the write history sync made before 00efd36: an on-demand chunk older
// than the stored chat, with EphemeralExpiration 0 because it reports no timer.
func TestStoreChatReplayZeroKeepsStoredTimerAndNewerTime(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	newer := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, time.September, 1, 8, 0, 0, 0, time.UTC)
	storeChatWithTimer(t, repo, timerChatJID, "Bob", newer, weekTimer)

	if err := repo.StoreChat(&domainChatStorage.Chat{
		DeviceID:            timerDeviceID,
		JID:                 timerChatJID,
		Name:                "Bob",
		LastMessageTime:     older,
		EphemeralExpiration: 0,
	}); err != nil {
		t.Fatalf("store replayed chat: %v", err)
	}

	chat := storedChat(t, repo, timerChatJID)
	if chat.EphemeralExpiration != weekTimer {
		t.Fatalf("replay overwrote the timer: stored %d, want %d", chat.EphemeralExpiration, weekTimer)
	}
	if !chat.LastMessageTime.Equal(newer) {
		t.Fatalf("replay moved LastMessageTime back to %s, want %s", chat.LastMessageTime, newer)
	}
}

func TestStoreChatExplicitZero(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	start := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	storeChatWithTimer(t, repo, timerChatJID, "Bob", start, weekTimer)

	storeChatWithTimer(t, repo, timerChatJID, "Bob", start, 0)
	if got := storedChat(t, repo, timerChatJID).EphemeralExpiration; got != 0 {
		t.Fatalf("explicit turn-off was not stored: stored %d, want 0", got)
	}

	newer := start.Add(time.Hour)
	if err := repo.StoreChat(&domainChatStorage.Chat{
		DeviceID:        timerDeviceID,
		JID:             timerChatJID,
		Name:            "Bob",
		LastMessageTime: newer,
	}); err != nil {
		t.Fatalf("store newer chat without a timer: %v", err)
	}
	chat := storedChat(t, repo, timerChatJID)
	if chat.EphemeralExpiration != 0 {
		t.Fatalf("a write without a timer changed the explicit 0 to %d", chat.EphemeralExpiration)
	}
	if !chat.LastMessageTime.Equal(newer) {
		t.Fatalf("LastMessageTime = %s, want the newer %s", chat.LastMessageTime, newer)
	}
}

// 234811c: a replay must keep the stored group name, not write "Group <id>".
func TestStoreChatReplayKeepsGroupName(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	groupJID := types.NewJID("120363000000000001", types.GroupServer)
	newer := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	storeChatWithTimer(t, repo, groupJID.String(), "Family", newer, weekTimer)

	name := repo.GetChatNameWithPushName(groupJID, groupJID.String(), "", "Family (synced subject)")
	if err := repo.StoreChat(&domainChatStorage.Chat{
		DeviceID:        timerDeviceID,
		JID:             groupJID.String(),
		Name:            name,
		LastMessageTime: newer.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatalf("store replayed group chat: %v", err)
	}

	chat := storedChat(t, repo, groupJID.String())
	if chat.Name != "Family" {
		t.Fatalf("replay renamed the group to %q", chat.Name)
	}
	if chat.EphemeralExpiration != weekTimer {
		t.Fatalf("replay overwrote the group timer: stored %d, want %d", chat.EphemeralExpiration, weekTimer)
	}
}

func TestCreateMessageStoresReportedTimerOnly(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	accountJID := types.NewJID("15550101000", types.DefaultUserServer)
	peerJID := types.NewJID("15550102000", types.DefaultUserServer)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(accountJID.String(), nil, repo))
	start := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	message := func(id string, at time.Time, msg *waE2E.Message) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: peerJID, Sender: peerJID},
				ID:            id,
				Timestamp:     at,
			},
			Message: msg,
		}
	}

	ephemeral := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text:        proto.String("disappears"),
		ContextInfo: &waE2E.ContextInfo{Expiration: proto.Uint32(weekTimer)},
	}}
	if err := repo.CreateMessage(ctx, message("ephemeral-1", start, ephemeral)); err != nil {
		t.Fatalf("create ephemeral message: %v", err)
	}
	if err := repo.CreateMessage(ctx, message("plain-1", start.Add(time.Minute), &waE2E.Message{Conversation: proto.String("no context")})); err != nil {
		t.Fatalf("create plain message: %v", err)
	}

	chat, err := repo.GetChatByDevice(accountJID.String(), peerJID.String())
	if err != nil || chat == nil {
		t.Fatalf("get chat: %v, %+v", err, chat)
	}
	if chat.EphemeralExpiration != weekTimer {
		t.Fatalf("live timer = %d, want %d", chat.EphemeralExpiration, weekTimer)
	}
}

// Many writers share one WAL database in production (five connections by
// default), so StoreChat must wait for the writer lock rather than fail.
func TestStoreChatConcurrentWritersKeepNewestTime(t *testing.T) {
	uri := sqlite.FormatChatStorageURI("file:"+filepath.Join(t.TempDir(), "chatstorage.db"), true, true)
	db, err := sql.Open(sqlite.DriverName, uri)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(5)
	repo := &SQLiteRepository{db: db}
	if err := repo.InitializeSchema(); err != nil {
		t.Fatalf("initialize schema: %v", err)
	}

	start := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	storeChatWithTimer(t, repo, timerChatJID, "Bob", start, weekTimer)

	const writers, writesPerWriter = 5, 8
	var wg sync.WaitGroup
	errs := make(chan error, writers*writesPerWriter)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range writesPerWriter {
				errs <- repo.StoreChat(&domainChatStorage.Chat{
					DeviceID:        timerDeviceID,
					JID:             timerChatJID,
					Name:            "Bob",
					LastMessageTime: start.Add(time.Duration(w*writesPerWriter+i+1) * time.Second),
				})
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent StoreChat: %v", err)
		}
	}

	chat := storedChat(t, repo, timerChatJID)
	if want := start.Add(writers * writesPerWriter * time.Second); !chat.LastMessageTime.Equal(want) {
		t.Fatalf("LastMessageTime = %s, want the newest %s", chat.LastMessageTime, want)
	}
	if chat.EphemeralExpiration != weekTimer {
		t.Fatalf("concurrent writes without a timer changed it to %d", chat.EphemeralExpiration)
	}
}
