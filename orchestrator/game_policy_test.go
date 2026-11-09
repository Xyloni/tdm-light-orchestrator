package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestGamePolicyQueueAndQuarantine(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "drop", Starts: now.Add(-time.Hour), Ends: now.Add(10 * time.Minute), Minutes: 30}
	g := &gameState{Name: "Example", Target: 1, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	s.state.Accounts = []account{{Game: "example", Credentials: parser.Account{Login: "player", UserID: "1", OAuth: "old"}}}
	s.command("game_policy Example | resources 25")
	if cfg := s.gameSettings(g); !cfg.SaveResources || cfg.ReserveMinutes != 25 || s.state.Settings.SaveResources {
		t.Fatal("per-game override did not preserve global defaults")
	}
	s.state.Settings.MaxGames = 1
	s.active["other"] = &activeGame{}
	if status := s.queueSummary("example", g, now); !strings.Contains(status, "свободный слот") || !strings.Contains(status, "Риск дедлайна") {
		t.Fatalf("missing wait reason or risk: %s", status)
	}
	delete(s.active, "other")
	s.observeGroupNotices(&activeGame{}, g, []worker.ManagedProgress{{Login: "player", Status: "error: auth"}})
	if len(pending(s.state, "example", c)) != 0 || !strings.Contains(s.command("quarantine"), "player") {
		t.Fatal("quarantined account should be visible and excluded from pending")
	}
	if err := s.upload([]byte("mail:player:password:new:1:device")); err != nil {
		t.Fatal(err)
	}
	saved, err := loadState(s.path)
	if err != nil || saved.Accounts[0].Quarantined || saved.Accounts[0].Game != "example" {
		t.Fatalf("new token should clear quarantine without moving the account: %v", err)
	}
}
