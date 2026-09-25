package chatstorage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	domain "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

var _ domain.IArchiveRepository = (*SQLiteRepository)(nil)

const archiveColumns = `id, chat_jid, device_id, sender, content, timestamp, is_from_me,
 media_type, call_metadata, filename, url, direct_path, media_key, file_sha256,
 file_enc_sha256, file_length, referral_metadata, created_at, updated_at`

func archiveWhere(f domain.ArchiveFilter) (string, []any) {
	clauses := []string{"device_id = ?"}
	args := []any{f.DeviceID}
	if f.ChatJID != "" {
		clauses = append(clauses, "chat_jid = ?")
		args = append(args, f.ChatJID)
	}
	if f.Sender != "" {
		clauses = append(clauses, "sender = ?")
		args = append(args, f.Sender)
	}
	if f.Search != "" {
		clauses = append(clauses, `LOWER(COALESCE(content,'')) LIKE ? ESCAPE '\'`)
		literal := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(f.Search))
		args = append(args, "%"+literal+"%")
	}
	if f.StartTime != nil {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, *f.StartTime)
	}
	if f.EndTime != nil {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, *f.EndTime)
	}
	if f.MediaOnly {
		clauses = append(clauses, "COALESCE(media_type,'') NOT IN ('','call')")
	}
	if f.MediaType != "" {
		clauses = append(clauses, "media_type = ?")
		args = append(args, f.MediaType)
	}
	if f.MessageType != "" {
		clauses = append(clauses, "COALESCE(NULLIF(media_type,''),'text') = ?")
		args = append(args, f.MessageType)
	}
	if f.IsFromMe != nil {
		clauses = append(clauses, "is_from_me = ?")
		args = append(args, *f.IsFromMe)
	}
	return strings.Join(clauses, " AND "), args
}

func (r *SQLiteRepository) readArchiveRows(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]*domain.Message, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*domain.Message, 0)
	for rows.Next() {
		m, err := r.scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (r *SQLiteRepository) QueryArchive(ctx context.Context, f domain.ArchiveFilter) ([]*domain.Message, int64, error) {
	if err := f.Validate(); err != nil {
		return nil, 0, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	where, args := archiveWhere(f)
	var total int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := " DESC"
	if f.Asc {
		order = " ASC"
	}
	query := "SELECT " + archiveColumns + " FROM messages WHERE " + where + " ORDER BY timestamp" + order + ", chat_jid" + order + ", id" + order + " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)
	result, err := r.readArchiveRows(ctx, tx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	// Release the snapshot before the legacy reaction loader opens a connection.
	// Keep chat+device in each batch: IDs alone are not globally unique.
	byChat := make(map[string][]*domain.Message)
	for _, m := range result {
		byChat[m.ChatJID] = append(byChat[m.ChatJID], m)
	}
	for chat, messages := range byChat {
		if err = ctx.Err(); err != nil {
			return nil, 0, err
		}
		if err = r.loadMessageReactions(f.DeviceID, chat, messages); err != nil {
			return nil, 0, err
		}
	}
	return result, total, nil
}

func (r *SQLiteRepository) ContextArchive(ctx context.Context, device, chat, id string, before, after int) ([]*domain.Message, *domain.Message, []*domain.Message, error) {
	if strings.TrimSpace(device) == "" || strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" || len(device) > 256 || len(chat) > 256 || len(id) > 256 || before < 0 || before > 100 || after < 0 || after > 100 {
		return nil, nil, nil, errors.New("device, chat, message_id and context windows 0..100 are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	anchor, err := r.scanMessage(tx.QueryRowContext(ctx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? AND id=?", device, chat, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, domain.ErrArchiveAnchorNotFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	prev, next := make([]*domain.Message, 0), make([]*domain.Message, 0)
	if before > 0 {
		prev, err = r.readArchiveRows(ctx, tx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? AND (timestamp<? OR (timestamp=? AND id<?)) ORDER BY timestamp DESC,id DESC LIMIT ?", device, chat, anchor.Timestamp, anchor.Timestamp, id, before)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if after > 0 {
		next, err = r.readArchiveRows(ctx, tx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? AND (timestamp>? OR (timestamp=? AND id>?)) ORDER BY timestamp ASC,id ASC LIMIT ?", device, chat, anchor.Timestamp, anchor.Timestamp, id, after)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	for i, j := 0, len(prev)-1; i < j; i, j = i+1, j-1 {
		prev[i], prev[j] = prev[j], prev[i]
	}
	all := append(append(append([]*domain.Message{}, prev...), anchor), next...)
	if err = r.loadMessageReactions(device, chat, all); err != nil {
		return nil, nil, nil, err
	}
	return prev, anchor, next, nil
}

func (r *SQLiteRepository) CoverageArchive(ctx context.Context, device, chat string) (domain.ArchiveCoverage, error) {
	out := domain.ArchiveCoverage{ChatJID: chat}
	if strings.TrimSpace(device) == "" || strings.TrimSpace(chat) == "" || len(device) > 256 || len(chat) > 256 {
		return out, errors.New("device and chat are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN COALESCE(media_type,'') NOT IN ('','call') THEN 1 ELSE 0 END),0) FROM messages WHERE device_id=? AND chat_jid=?`, device, chat).Scan(&out.MessageCount, &out.MediaCount)
	if err != nil {
		return out, err
	}
	if out.MessageCount > 0 {
		oldest, e := r.scanMessage(tx.QueryRowContext(ctx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? ORDER BY timestamp ASC,id ASC LIMIT 1", device, chat))
		if e != nil {
			return out, e
		}
		newest, e := r.scanMessage(tx.QueryRowContext(ctx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? ORDER BY timestamp DESC,id DESC LIMIT 1", device, chat))
		if e != nil {
			return out, e
		}
		out.OldestTimestamp = &oldest.Timestamp
		out.NewestTimestamp = &newest.Timestamp
		anchor, e := r.scanMessage(tx.QueryRowContext(ctx, "SELECT "+archiveColumns+" FROM messages WHERE device_id=? AND chat_jid=? AND COALESCE(media_type,'')!='call' ORDER BY timestamp ASC,id ASC LIMIT 1", device, chat))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		if e == nil {
			out.AnchorMessageID = anchor.ID
			out.AnchorTimestamp = &anchor.Timestamp
		}
	}
	return out, tx.Commit()
}
