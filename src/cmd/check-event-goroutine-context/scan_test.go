package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanEventGoroutines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("event_hit.go", `package whatsapp
import "context"
func f(ctx context.Context) {
	context.Background()
	go func() {
		context.WithTimeout(context.Background(), 0)
	}()
	go helper(context.Background())
}
func helper(context.Context) {}
`)
	write("event_clean.go", `package whatsapp
import "context"
func f(ctx context.Context) {
	go func() {
		context.WithTimeout(context.WithoutCancel(ctx), 0)
	}()
}
`)
	write("event_alias.go", `package whatsapp
import ctx "context"
func f() {
	go func() {
		ctx.Background()
	}()
}
`)
	write("event_dot.go", `package whatsapp
import . "context"
func f() {
	go func() {
		Background()
	}()
}
`)
	write("event_nested.go", `package whatsapp
import "context"
func f(ctx context.Context) {
	go func() {
		go func() {
			context.Background()
		}()
	}()
}
`)
	write("event_sample_test.go", `package whatsapp
import "context"
func f() {
	go func() {
		context.Background()
	}()
}
`)
	write("message.go", `package whatsapp
import "context"
func f() {
	go func() {
		context.Background()
	}()
}
`)

	hits, err := scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.path]++
	}
	want := map[string]int{
		"event_hit.go":         2,
		"event_alias.go":       1,
		"event_dot.go":         1,
		"event_nested.go":      1,
		"event_sample_test.go": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("hits %#v", got)
	}
	for path, count := range want {
		if got[path] != count {
			t.Fatalf("%s hits = %d, want %d (%#v)", path, got[path], count, got)
		}
	}
}

func TestScanMissingDir(t *testing.T) {
	if _, err := scan(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestScanEmptyDir(t *testing.T) {
	if _, err := scan(t.TempDir()); err == nil {
		t.Fatal("expected an error when the directory has no event_*.go files")
	}
}
