package chatstorage

import (
	"context"
	"testing"
	"time"

	domain "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/stretchr/testify/require"
)

func TestReactionScopeMigrationPreservesExistingRows(t *testing.T) {
	r := newTestSQLiteRepository(t)
	now := time.Now().UTC()
	// Only this temporary test database is downgraded to the historical schema.
	_, err := r.db.Exec("DROP TABLE message_reactions")
	require.NoError(t, err)
	_, err = r.db.Exec(r.getMigrations()[16])
	require.NoError(t, err)
	_, err = r.db.Exec("INSERT INTO message_reactions(message_id,chat_jid,device_id,reactor_jid,emoji,reaction_timestamp) VALUES(?,?,?,?,?,?)", "same", "a@g.us", "111", "222", "original", now)
	require.NoError(t, err)
	_, err = r.db.Exec("DELETE FROM gowa_fork_schema_info WHERE migration_id=?", reactionScopeMigrationID)
	require.NoError(t, err)
	versionBefore, err := r.getSchemaVersion()
	require.NoError(t, err)
	require.NoError(t, r.applyForkMigrations())
	versionAfter, err := r.getSchemaVersion()
	require.NoError(t, err)
	require.Equal(t, versionBefore, versionAfter)
	var emoji string
	require.NoError(t, r.db.QueryRow("SELECT emoji FROM message_reactions WHERE chat_jid='a@g.us'").Scan(&emoji))
	require.Equal(t, "original", emoji)
	require.NoError(t, r.StoreReaction(&domain.Reaction{MessageID: "same", ChatJID: "b@g.us", DeviceID: "111", ReactorJID: "222", Emoji: "second", Timestamp: now}))
	var count int
	require.NoError(t, r.db.QueryRow("SELECT COUNT(*) FROM message_reactions").Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, r.InitializeSchema())
}
func TestReactionUpdateAndRemovalStayInsideChat(t *testing.T) {
	r := newTestSQLiteRepository(t)
	now := time.Now().UTC()
	device := "111"
	for _, chat := range []string{"a@g.us", "b@g.us"} {
		seedChatMessage(t, r, device, chat, "same", "fixture", now)
		require.NoError(t, r.StoreReaction(&domain.Reaction{MessageID: "same", ChatJID: chat, DeviceID: device, ReactorJID: "222", Emoji: chat, Timestamp: now}))
	}
	require.NoError(t, r.StoreReaction(&domain.Reaction{MessageID: "same", ChatJID: "a@g.us", DeviceID: device, ReactorJID: "222", Emoji: "changed", Timestamp: now}))
	require.NoError(t, r.StoreReaction(&domain.Reaction{MessageID: "same", ChatJID: "a@g.us", DeviceID: device, ReactorJID: "222", Emoji: "", Timestamp: now}))
	rows, _, err := r.QueryArchive(context.Background(), domain.ArchiveFilter{DeviceID: device, Limit: 10})
	require.NoError(t, err)
	for _, m := range rows {
		if m.ChatJID == "a@g.us" {
			require.Empty(t, m.Reactions)
		} else {
			require.Len(t, m.Reactions, 1)
			require.Equal(t, "b@g.us", m.Reactions[0].Emoji)
		}
	}
}
