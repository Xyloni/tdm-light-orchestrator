package orchestrator

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestArchiveDailyLogsSelectsOnlyRequestedDay(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"orchestrator-2026-11-09.log",
		"game-fixture/account-2026-11-09.log.1",
		"game-fixture/account-2026-11-08.log",
		"account.txt",
		"state.json",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	name, data, message := archiveDailyLogs(context.Background(), root, "2026-11-09")
	if name != "tdm-logs-2026-11-09.zip" || message != "" {
		t.Fatalf("name=%q message=%q", name, message)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, file := range archive.File {
		entries = append(entries, file.Name)
	}
	if want := []string{"game-fixture/account-2026-11-09.log.1", "orchestrator-2026-11-09.log"}; !slices.Equal(entries, want) {
		t.Fatalf("archive entries: %v, want %v", entries, want)
	}
}
