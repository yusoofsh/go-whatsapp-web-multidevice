// Package mcpstore provides private device-scoped attachments and a reference-only
// event journal. It never stores WhatsApp encryption keys or event message bodies.
package mcpstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"github.com/google/uuid"
)

const MaxMediaBytes = 10 << 20
const MaxRequestBytes = 15 << 20
const EventResource = "whatsapp://events"

type Event struct {
	ID        int64  `json:"id"`
	DeviceID  string `json:"device_id"`
	Type      string `json:"type"`
	ChatJID   string `json:"chat_jid,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Timestamp int64  `json:"timestamp"`
}
type EventPage struct {
	Events        []Event `json:"events"`
	NextCursor    int64   `json:"next_cursor"`
	HasMore       bool    `json:"has_more"`
	CursorExpired bool    `json:"cursor_expired"`
	PurgedThrough int64   `json:"purged_through"`
}
type Media struct {
	ID        string `json:"media_id"`
	URI       string `json:"uri"`
	Filename  string `json:"filename"`
	MIME      string `json:"mime_type"`
	Size      int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ExpiresAt int64  `json:"expires_at"`
}
type Store struct {
	db            *sql.DB
	mu            sync.Mutex
	listeners     map[string]func(Event)
	now           func() time.Time
	maxMediaBytes int
	mediaBudget   int64
	lastPrune     time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	db, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI(path, true, true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS mcp_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, device_id TEXT NOT NULL, type TEXT NOT NULL,
 chat_jid TEXT NOT NULL, message_id TEXT NOT NULL, ts INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS mcp_events_device_cursor ON mcp_events(device_id,id);
 CREATE INDEX IF NOT EXISTS mcp_events_time ON mcp_events(ts);
 CREATE TABLE IF NOT EXISTS mcp_event_watermarks (device_id TEXT PRIMARY KEY, purged_through INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS mcp_media (id TEXT PRIMARY KEY, device_id TEXT NOT NULL,
 filename TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, sha256 TEXT NOT NULL,
 expires INTEGER NOT NULL, data BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS mcp_media_expiry ON mcp_media(expires);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, listeners: make(map[string]func(Event)), now: time.Now, maxMediaBytes: MaxMediaBytes, mediaBudget: 256 << 20}
	if err = s.Prune(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func ValidateMedia(filename, mediaType string, data []byte, max int) (string, error) {
	if filename == "" || len(filename) > 160 || strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." || strings.ContainsFunc(filename, unicode.IsControl) {
		return "", errors.New("invalid attachment filename")
	}
	if len(data) == 0 || len(data) > max {
		return "", fmt.Errorf("attachment must contain 1..%d bytes", max)
	}
	if len(mediaType) > 120 {
		return "", errors.New("invalid MIME type")
	}
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err != nil || !strings.Contains(parsed, "/") {
		return "", errors.New("invalid MIME type")
	}
	parsed = strings.ToLower(parsed)
	sniff := http.DetectContentType(data)
	if activeMIME(parsed) || activeMIME(strings.Split(sniff, ";")[0]) {
		return "", errors.New("active HTML, XML, SVG or script attachments are not accepted")
	}
	if strings.HasPrefix(parsed, "image/") && strings.Split(sniff, ";")[0] != parsed {
		return "", errors.New("image MIME does not match its bytes")
	}
	return parsed, nil
}
func activeMIME(m string) bool {
	// OOXML Office MIME names contain "xml", but describe ZIP documents,
	// not browser-executable XML. Only block actual active media types.
	return m == "text/html" || m == "application/xhtml+xml" || m == "image/svg+xml" ||
		m == "text/xml" || m == "application/xml" || strings.HasSuffix(m, "+xml") ||
		m == "application/javascript" || m == "text/javascript" ||
		m == "application/ecmascript" || m == "text/ecmascript"
}
func (s *Store) Stage(ctx context.Context, device, filename, mediaType string, data []byte) (Media, error) {
	if device == "" {
		return Media{}, errors.New("missing device scope")
	}
	mediaType, err := ValidateMedia(filename, mediaType, data, s.maxMediaBytes)
	if err != nil {
		return Media{}, err
	}
	sum := sha256.Sum256(data)
	m := Media{ID: uuid.NewString(), Filename: filename, MIME: mediaType, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), ExpiresAt: s.now().Add(24 * time.Hour).Unix()}
	m.URI = "whatsapp://media/" + m.ID
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Media{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_media WHERE expires<=?", s.now().Unix()); err != nil {
		return Media{}, err
	}
	var total, count int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0),COUNT(*) FROM mcp_media").Scan(&total, &count); err != nil {
		return Media{}, err
	}
	if total+m.Size > s.mediaBudget || count >= 1024 {
		return Media{}, errors.New("MCP attachment quota reached; wait for staged attachments to expire")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO mcp_media VALUES(?,?,?,?,?,?,?,?)", m.ID, device, m.Filename, m.MIME, m.Size, m.SHA256, m.ExpiresAt, data)
	if err != nil {
		return Media{}, err
	}
	if err = tx.Commit(); err != nil {
		return Media{}, err
	}
	return m, nil
}
func (s *Store) Media(ctx context.Context, device, id string) (Media, []byte, error) {
	if device == "" {
		return Media{}, nil, errors.New("missing device scope")
	}
	if _, err := uuid.Parse(id); err != nil {
		return Media{}, nil, errors.New("invalid media_id")
	}
	var m Media
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT id,filename,mime,size,sha256,expires,data FROM mcp_media WHERE device_id=? AND id=? AND expires>?", device, id, s.now().Unix()).Scan(&m.ID, &m.Filename, &m.MIME, &m.Size, &m.SHA256, &m.ExpiresAt, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, nil, errors.New("attachment not found for this device or expired")
	}
	if err != nil {
		return Media{}, nil, err
	}
	m.URI = "whatsapp://media/" + m.ID
	return m, data, nil
}

func (s *Store) Append(ctx context.Context, e Event) (Event, error) {
	if e.DeviceID == "" || e.Type == "" || len(e.DeviceID) > 256 || len(e.Type) > 64 || len(e.ChatJID) > 256 || len(e.MessageID) > 256 {
		return Event{}, errors.New("invalid event metadata")
	}
	e.Timestamp = s.now().Unix()
	s.mu.Lock()
	if s.now().Sub(s.lastPrune) >= time.Hour {
		if err := s.pruneLocked(ctx); err != nil {
			s.mu.Unlock()
			return Event{}, err
		}
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO mcp_events(device_id,type,chat_jid,message_id,ts) VALUES(?,?,?,?,?)", e.DeviceID, e.Type, e.ChatJID, e.MessageID, e.Timestamp)
	if err != nil {
		s.mu.Unlock()
		return Event{}, err
	}
	e.ID, err = r.LastInsertId()
	listeners := make([]func(Event), 0, len(s.listeners))
	for _, fn := range s.listeners {
		listeners = append(listeners, fn)
	}
	if err == nil && e.ID%1024 == 0 {
		err = s.pruneLocked(ctx)
	}
	s.mu.Unlock()
	if err != nil {
		return Event{}, err
	}
	for _, fn := range listeners {
		fn(e)
	}
	return e, nil
}
func (s *Store) Events(ctx context.Context, device string, after int64, limit int) (EventPage, error) {
	p := EventPage{Events: []Event{}, NextCursor: after}
	if device == "" || after < 0 || limit < 1 || limit > 500 {
		return p, errors.New("device, nonnegative cursor and limit 1..500 required")
	}
	s.mu.Lock()
	var pruneErr error
	if s.now().Sub(s.lastPrune) >= time.Hour {
		pruneErr = s.pruneLocked(ctx)
	}
	s.mu.Unlock()
	if pruneErr != nil {
		return p, pruneErr
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, "SELECT purged_through FROM mcp_event_watermarks WHERE device_id=?", device).Scan(&p.PurgedThrough)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.CursorExpired = after > 0 && after < p.PurgedThrough
	rows, err := tx.QueryContext(ctx, "SELECT id,device_id,type,chat_jid,message_id,ts FROM mcp_events WHERE device_id=? AND id>? ORDER BY id LIMIT ?", device, after, limit+1)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.DeviceID, &e.Type, &e.ChatJID, &e.MessageID, &e.Timestamp); err != nil {
			_ = rows.Close()
			return p, err
		}
		p.Events = append(p.Events, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return p, err
	}
	if len(p.Events) > limit {
		p.HasMore = true
		p.Events = p.Events[:limit]
	}
	if len(p.Events) > 0 {
		p.NextCursor = p.Events[len(p.Events)-1].ID
	}
	return p, tx.Commit()
}
func (s *Store) Listen(fn func(Event)) func() {
	id := uuid.NewString()
	s.mu.Lock()
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.listeners, id); s.mu.Unlock() }
}
func (s *Store) Prune(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(ctx)
}
func (s *Store) pruneLocked(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := s.now().Add(-7 * 24 * time.Hour).Unix()
	const old = "ts<? OR id<=COALESCE((SELECT id FROM mcp_events ORDER BY id DESC LIMIT 1 OFFSET 99999),0)"
	_, err = tx.ExecContext(ctx, "INSERT INTO mcp_event_watermarks(device_id,purged_through) SELECT device_id,MAX(id) FROM mcp_events WHERE "+old+" GROUP BY device_id ON CONFLICT(device_id) DO UPDATE SET purged_through=MAX(purged_through,excluded.purged_through)", cutoff)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_events WHERE "+old, cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_media WHERE expires<=?", s.now().Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.lastPrune = s.now()
	}
	return err
}
