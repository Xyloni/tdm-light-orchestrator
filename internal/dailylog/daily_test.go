package dailylog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneKeepsThreeDaysAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.Local)
	names := map[string]bool{"miner-2026-11-06.log": false, "miner-2026-11-06.log.1": false, "miner-2026-11-07.log": true, "miner-2026-11-09.log": true, "state.json": true}
	for name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prune(root, now); err != nil {
		t.Fatal(err)
	}
	for name, keep := range names {
		_, err := os.Stat(filepath.Join(root, name))
		if (err == nil) != keep {
			t.Errorf("%s: keep=%t, stat=%v", name, keep, err)
		}
	}
}
