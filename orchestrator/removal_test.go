package orchestrator

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestRemoveActiveGameWaitsBeforeReturningAccounts(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	s.state.Games["example"] = &gameState{Name: "Example"}
	s.state.Accounts = []account{{Credentials: parser.Account{Login: "player"}, Game: "example"}}
	stopped := false
	miner := &minerProcess{stop: func() { stopped = true }}
	s.active["example"] = &activeGame{process: miner}
	form := url.Values{"action": {"remove"}, "game": {"example"}}
	s.panelAction(form)
	if stopped {
		t.Fatal("unconfirmed removal stopped the miner")
	}
	form.Set("confirm", "on")
	form.Set("disposition", "release")
	s.panelAction(form)
	if !stopped || !s.state.Games["example"].Removing || s.state.Accounts[0].Game != "example" {
		t.Fatal("pool released before worker exit")
	}
	s.tick(context.Background(), now)
	if s.state.Games["example"] == nil {
		t.Fatal("game removed while process was still active")
	}
	s.update(processUpdate{game: "example", process: miner, exited: true}, now)
	s.tick(context.Background(), now)
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Games["example"] != nil || len(saved.Accounts) != 1 || saved.Accounts[0].Game != "" {
		t.Fatal("removal and pool release were not saved")
	}
}
