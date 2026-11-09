package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoredDropNamesPriorityAndMatching(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "config.json")
	envFile := filepath.Join(dir, "config.env")
	if err := os.WriteFile(jsonFile, []byte(`{"target_game":"Game","ignored_drop_names":["JSON reward"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("IGNORED_DROP_NAMES=Hunt White, Other reward\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(jsonFile, envFile)
	if err != nil || !cfg.DropIgnored(" hunt WHITE ") || !cfg.DropIgnored("Other reward") || cfg.DropIgnored("Hunt White Extra") || cfg.DropIgnored("JSON reward") {
		t.Fatal("name matching or file precedence", err)
	}
	t.Setenv("IGNORED_DROP_NAMES", `["Name, with comma"]`)
	cfg, err = Load(jsonFile, envFile)
	if err != nil || !cfg.DropIgnored("Name, with comma") || cfg.DropIgnored("Hunt White") {
		t.Fatal("JSON environment override", err)
	}
	t.Setenv("IGNORED_DROP_NAMES", "")
	cfg, err = Load(jsonFile, envFile)
	if err != nil || len(cfg.IgnoredDropNames) != 0 {
		t.Fatal("cannot clear default exclusions", err)
	}
	t.Setenv("IGNORED_DROP_NAMES", "[invalid")
	if _, err = Load(jsonFile, envFile); err == nil {
		t.Fatal("malformed exclusions accepted")
	}
}

func TestHuntWhiteDefaultIsExplicitlyReversible(t *testing.T) {
	if !Default().DropIgnored("Hunt White") {
		t.Fatal("default exclusion missing")
	}
	cfg := Default()
	cfg.IgnoredDropNames = nil
	if cfg.DropIgnored("Hunt White") {
		t.Fatal("exclusion is hardcoded outside config")
	}
}
