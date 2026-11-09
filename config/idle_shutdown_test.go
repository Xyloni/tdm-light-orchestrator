package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdleShutdownConfigPriorityAndDefaults(t *testing.T) {
	dir := t.TempDir()
	j, e := filepath.Join(dir, "config.json"), filepath.Join(dir, "config.env")
	if err := os.WriteFile(j, []byte(`{"target_game":"Game","stop_when_idle":true,"idle_check_rounds":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e, []byte("IDLE_CHECK_ROUNDS=4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IDLE_CHECK_ROUNDS", "3")
	cfg, err := Load(j, e)
	if err != nil || !cfg.StopWhenIdle || cfg.IdleCheckRounds != 3 {
		t.Fatal("config precedence failed", err)
	}
	t.Setenv("IDLE_CHECK_ROUNDS", "0")
	if _, err := Load(j, e); err == nil {
		t.Fatal("zero rounds accepted")
	}
	if Default().StopWhenIdle || Default().IdleCheckRounds != 3 {
		t.Fatal("unexpected default shutdown policy")
	}
}
