package chatstorage

// Appended after the existing migration sequence. Rebuild atomically under
// runMigration's transaction; all existing rows and timestamps are preserved.
const reactionScopeMigration = `
CREATE TABLE message_reactions_scoped (
 message_id VARCHAR(255) NOT NULL,
 chat_jid VARCHAR(255) NOT NULL,
 device_id VARCHAR(255) NOT NULL DEFAULT '',
 reactor_jid VARCHAR(255) NOT NULL,
 emoji TEXT NOT NULL DEFAULT '',
 is_from_me BOOLEAN DEFAULT FALSE,
 reaction_timestamp TIMESTAMP NOT NULL,
 created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(message_id,chat_jid,reactor_jid,device_id)
);
INSERT INTO message_reactions_scoped
 (message_id,chat_jid,device_id,reactor_jid,emoji,is_from_me,reaction_timestamp,created_at,updated_at)
 SELECT message_id,chat_jid,device_id,reactor_jid,emoji,is_from_me,reaction_timestamp,created_at,updated_at FROM message_reactions;
DROP TABLE message_reactions;
ALTER TABLE message_reactions_scoped RENAME TO message_reactions;
CREATE INDEX idx_message_reactions_lookup ON message_reactions(device_id,chat_jid,message_id);
`

func (r *SQLiteRepository) deleteReactionInChat(messageID, chatJID, reactorJID, deviceID string) error {
	_, err := r.db.Exec("DELETE FROM message_reactions WHERE message_id=? AND chat_jid=? AND reactor_jid=? AND device_id=?", messageID, chatJID, reactorJID, deviceID)
	return err
}
