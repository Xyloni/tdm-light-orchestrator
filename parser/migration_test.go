package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyFile(t *testing.T) {
	for _, targetExists := range []bool{false, true} {
		dir := t.TempDir()
		legacy, target := filepath.Join(dir, "accounts.txt"), filepath.Join(dir, "account.txt")
		if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
			t.Fatal(err)
		}
		want := "legacy"
		if targetExists {
			want = "current"
			if err := os.WriteFile(target, []byte(want), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := MigrateLegacyFile(target); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, error %v; want %q", got, err, want)
		}
		if err := MigrateLegacyFile(target); err != nil {
			t.Fatal(err)
		}
	}
}
