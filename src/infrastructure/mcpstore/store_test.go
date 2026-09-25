package mcpstore

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func TestMediaRoundTripIsolationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7\nexample")
	m, err := s.Stage(context.Background(), "a", "report.pdf", "application/pdf", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Media(context.Background(), "b", m.ID); err == nil {
		t.Fatal("cross-device read allowed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, raw, err := s.Media(context.Background(), "a", m.ID)
	if err != nil || !bytes.Equal(raw, data) || got.SHA256 == "" || got.URI != "whatsapp://media/"+m.ID {
		t.Fatalf("roundtrip: %#v %v", got, err)
	}
}
func TestMediaValidationAndQuota(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, name := range []string{"../secret", "a/b", `a\b`, "", "a\x00b", "a\nb"} {
		if _, err := s.Stage(ctx, "a", name, "application/pdf", []byte("%PDF-1.7")); err == nil {
			t.Errorf("unsafe filename %q", name)
		}
	}
	for _, mt := range []string{"text/html", "image/svg+xml", "application/javascript", "not a mime"} {
		if _, err := s.Stage(ctx, "a", "file", mt, []byte("x")); err == nil {
			t.Errorf("unsafe MIME %q", mt)
		}
	}
	if _, err := s.Stage(ctx, "a", "x.png", "image/png", []byte("<html>active</html>")); err == nil {
		t.Fatal("MIME spoof accepted")
	}
	if _, err := s.Stage(ctx, "", "x.txt", "text/plain", []byte("x")); err == nil {
		t.Fatal("empty scope accepted")
	}
	s.maxMediaBytes = 8
	if _, err := s.Stage(ctx, "a", "x.txt", "text/plain", []byte("123456789")); err == nil {
		t.Fatal("oversize accepted")
	}
	s.maxMediaBytes = 32
	s.mediaBudget = 10
	if _, err := s.Stage(ctx, "a", "x.txt", "text/plain", []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stage(ctx, "a", "y.txt", "text/plain", []byte("1234")); err == nil {
		t.Fatal("quota exceeded")
	}
}
func TestEventsScopedDurableCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(context.Background(), Event{DeviceID: "b", Type: "message", MessageID: "hidden"})
	first, err := s.Append(context.Background(), Event{DeviceID: "a", Type: "message", MessageID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(context.Background(), Event{DeviceID: "b", Type: "message", MessageID: "hidden2"})
	_, _ = s.Append(context.Background(), Event{DeviceID: "a", Type: "message.edited", MessageID: "1"})
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.Events(context.Background(), "a", 0, 1)
	if err != nil || len(page.Events) != 1 || page.NextCursor != first.ID || !page.HasMore {
		t.Fatalf("page %#v %v", page, err)
	}
	page, err = s.Events(context.Background(), "a", page.NextCursor, 100)
	if err != nil || len(page.Events) != 1 || page.Events[0].Type != "message.edited" {
		t.Fatalf("next %#v %v", page, err)
	}
	last := page.NextCursor
	page, err = s.Events(context.Background(), "a", last, 100)
	if err != nil || len(page.Events) != 0 || page.NextCursor != last {
		t.Fatal("duplicate replay")
	}
	if _, err = s.Events(context.Background(), "a", -1, 100); err == nil {
		t.Fatal("negative cursor")
	}
}
func TestExpiryAndRetentionWatermark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	m, err := s.Stage(ctx, "a", "x.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Append(ctx, Event{DeviceID: "a", Type: "message"})
	_, _ = s.Append(ctx, Event{DeviceID: "a", Type: "message"})
	now = now.Add(8 * 24 * time.Hour)
	if _, _, err = s.Media(ctx, "a", m.ID); err == nil {
		t.Fatal("expired media returned")
	}
	if err = s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.Events(ctx, "a", a.ID, 10)
	if err != nil || !page.CursorExpired || len(page.Events) != 0 {
		t.Fatalf("missing gap %#v %v", page, err)
	}
}
func TestEventListenersAndConcurrency(t *testing.T) {
	s := testStore(t)
	var mu sync.Mutex
	count := 0
	stop := s.Listen(func(Event) { mu.Lock(); count++; mu.Unlock() })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Append(context.Background(), Event{DeviceID: "a", Type: "message"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	stop()
	stop()
	_, _ = s.Append(context.Background(), Event{DeviceID: "a", Type: "message"})
	mu.Lock()
	defer mu.Unlock()
	if count != 20 {
		t.Fatalf("got %d", count)
	}
}
