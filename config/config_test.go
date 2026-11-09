package config

import (
	"path/filepath"
	"testing"
)

func TestLegacyAccountsPath(t *testing.T) {
	for _, name := range []string{"accounts.txt", filepath.Join("data", "accounts.txt"), "custom.txt"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TARGET_GAME", "Test")
			t.Setenv("ACCOUNTS_FILE", name)
			cfg, err := Load(filepath.Join(t.TempDir(), "missing.json"), filepath.Join(t.TempDir(), "missing.env"))
			if err != nil {
				t.Fatal(err)
			}
			want := name
			if filepath.Base(name) == "accounts.txt" {
				want = filepath.Join(filepath.Dir(name), "account.txt")
			}
			if cfg.AccountsFile != want {
				t.Fatalf("got %q, want %q", cfg.AccountsFile, want)
			}
		})
	}
}

func TestDefaultAccountFile(t *testing.T) {
	if Default().AccountsFile != "account.txt" {
		t.Fatal("default account filename is incorrect")
	}
}
