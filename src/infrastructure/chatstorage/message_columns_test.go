package chatstorage

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var messageColumnListPattern = regexp.MustCompile(`(?s)id\s*,\s*chat_jid\s*,\s*device_id\s*,\s*sender\s*,\s*content\s*,\s*timestamp\s*,\s*is_from_me\b(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)+`)

var archiveUsesMessageColumns = regexp.MustCompile(`const\s+archiveColumns\s*=\s*messageColumns\b`)

func TestArchiveAndMessageColumnListsMatch(t *testing.T) {
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate chatstorage source")
	}
	dir := filepath.Dir(caller)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var archiveSource []byte
	var literals []columnList
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "archive_repository.go" {
			archiveSource = body
		}
		for _, match := range messageColumnListPattern.FindAll(body, -1) {
			literals = append(literals, columnList{file: name, columns: columnNames(string(match))})
		}
	}
	if len(literals) == 0 {
		t.Fatal("no message column list found")
	}

	messageColumns := literals[0].columns
	var archiveColumns []string
	archiveAliased := archiveUsesMessageColumns.Match(archiveSource)
	if archiveAliased {
		for _, list := range literals {
			if list.file == "archive_repository.go" {
				t.Fatalf("archive_repository.go still has its own column list: %s", strings.Join(list.columns, ", "))
			}
			if !slices.Equal(list.columns, messageColumns) {
				t.Fatalf("message column lists differ\n%s: %s\n%s: %s", literals[0].file, strings.Join(messageColumns, ", "), list.file, strings.Join(list.columns, ", "))
			}
		}
		archiveColumns = messageColumns
	} else {
		var messageLists []columnList
		for _, list := range literals {
			if list.file == "archive_repository.go" {
				archiveColumns = list.columns
				continue
			}
			messageLists = append(messageLists, list)
		}
		if archiveColumns == nil {
			t.Fatal("archiveColumns does not use messageColumns")
		}
		if len(messageLists) == 0 {
			t.Fatal("no message column list found")
		}
		messageColumns = messageLists[0].columns
		for _, list := range messageLists[1:] {
			if !slices.Equal(list.columns, messageColumns) {
				t.Fatalf("message column lists differ\n%s: %s\n%s: %s", messageLists[0].file, strings.Join(messageColumns, ", "), list.file, strings.Join(list.columns, ", "))
			}
		}
	}

	if !slices.Contains(archiveColumns, "context_metadata") && slices.Contains(messageColumns, "context_metadata") {
		t.Fatal("context_metadata is missing on archive")
	}
	if !slices.Equal(archiveColumns, messageColumns) {
		t.Fatalf("archive column list drifted from the message column list\narchive: %s\nmessage: %s", strings.Join(archiveColumns, ", "), strings.Join(messageColumns, ", "))
	}
	if len(literals) != 1 {
		t.Fatalf("column-list literals = %d, want 1", len(literals))
	}
	if !archiveAliased {
		t.Fatal("archiveColumns does not use messageColumns")
	}
}

type columnList struct {
	file    string
	columns []string
}

func columnNames(list string) []string {
	parts := strings.Split(list, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
