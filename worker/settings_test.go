package worker

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestSettingsPersistWithoutMutatingActiveFarmAndReloadAfterGroup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")
	if err := os.WriteFile(file, []byte("TARGET_GAME=Game\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	m := NewManager(cfg, [][]*parser.Account{{{Login: "fixture"}}}, make(chan struct{}, 1))
	panel, reload := m.settingsAt(file, "STOP_WHEN_IDLE true")
	if reload || cfg.StopWhenIdle || !strings.Contains(panel.Text, "сейчас false") {
		t.Fatal("save changed running configuration", panel.Text)
	}
	m.settingsAt(file, "IDLE_CHECK_ROUNDS 4")
	stored, err := config.Load(filepath.Join(dir, "config.json"), file)
	if err != nil || !stored.StopWhenIdle || stored.IdleCheckRounds != 4 {
		t.Fatal("settings not persisted", err)
	}
	m.settingsAt(file, "IDLE_CHECK_ROUNDS 0")
	m.settingsAt(file, "TELEGRAM_BOT_TOKEN secret")
	stored, err = config.Load(filepath.Join(dir, "config.json"), file)
	if err != nil || stored.IdleCheckRounds != 4 || stored.TelegramBotToken != "" {
		t.Fatal("invalid setting accepted")
	}
	if _, reload = m.settingsAt(file, "apply after"); reload {
		t.Fatal("deferred apply interrupted farming")
	}
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) { return reviewReply(completedGroupInventory), nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !m.runCycles(ctx, time.Millisecond) || ctx.Err() != nil {
		t.Fatal("deferred apply did not reload after the group")
	}
}

func TestSettingsRejectEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")
	os.WriteFile(file, []byte("TARGET_GAME=Game\n"), 0600)
	t.Setenv("DIAGNOSTICS", "false")
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	m := NewManager(cfg, nil, make(chan struct{}, 1))
	panel, _ := m.settingsAt(file, "DIAGNOSTICS true")
	if !strings.Contains(panel.Text, "закреплена") {
		t.Fatal(panel.Text)
	}
	data, _ := os.ReadFile(file)
	if strings.Contains(string(data), "DIAGNOSTICS") {
		t.Fatal("locked setting saved")
	}
}
