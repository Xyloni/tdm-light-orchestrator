package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestPartialGroupExportOnlyStopsChosenAccountsAfterSend(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "event", Name: "Event", Minutes: 60, Group: []string{"fast", "slow", "finished"}}
	s.state.Games["game"] = &gameState{Name: "Game", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	for _, login := range []string{"fast", "slow", "finished", "queued"} {
		s.state.Accounts = append(s.state.Accounts, account{Game: "game", Credentials: parser.Account{Login: login}})
	}
	process := &minerProcess{commands: make(chan minerCommand, 1)}
	a := &activeGame{process: process, groupStarted: now, progress: []worker.ManagedProgress{
		{Login: "fast", Current: 45, Required: 60},
		{Login: "slow", Current: 10, Required: 60},
		{Login: "finished", Status: "idle"},
	}}
	s.active["game"] = a
	ref := gameRef("game")
	if panel := s.previewPartialExport(ref, 3, now); !strings.Contains(panel.Text, "Доступно для снятия: 2") {
		t.Fatalf("invalid count was accepted: %q", panel.Text)
	}
	panel := s.telegramMenu("partial:" + ref + "\n1")
	doc := s.telegramMenu(panel.Buttons[0][0].Action).Document
	if doc == nil || doc.AfterSend == nil || !strings.Contains(string(doc.Data), ":slow:") ||
		strings.Contains(string(doc.Data), ":fast:") || len(process.commands) != 0 || len(s.state.Accounts) != 4 {
		t.Fatal("export selected the wrong accounts or modified the group before delivery")
	}
	a.progress[1].Status = "idle"
	if result := doc.AfterSend(); !strings.Contains(result, "отменено") || len(process.commands) != 0 || len(s.state.Accounts) != 4 {
		t.Fatal("a completed account was removed after the selection became stale")
	}
	a.progress[1].Status = ""
	panel = s.previewPartialExport(ref, 1, now)
	doc = s.telegramMenu(panel.Buttons[0][0].Action).Document
	if doc == nil || doc.AfterSend == nil {
		t.Fatal("fresh confirmation did not return the export")
	}
	doc.AfterSend()
	if len(process.commands) != 1 || len(s.state.Accounts) != 3 || c.Results["slow"] != "trimmed" || c.Results["fast"] != "" {
		t.Fatal("expected exactly one account to be removed")
	}
	cmd := <-process.commands
	if len(cmd.Remove) != 1 || cmd.Remove[0] != "slow" {
		t.Fatalf("unexpected stop command: %+v", cmd)
	}
}
