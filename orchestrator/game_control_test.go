package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestGameControlPreservesGroupAndPersistedPause(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "drop", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Minutes: 30, Group: []string{"player"}}
	g := &gameState{Name: "Example", Target: 1, Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	s.state.Accounts = []account{{Credentials: parser.Account{Login: "player"}, Game: "example"}}
	stops := 0
	miner := &minerProcess{commands: make(chan minerCommand, 1), stop: func() { stops++ }}
	active := &activeGame{process: miner, groupStarted: now}
	s.active["example"] = active
	s.command("restart Example")
	if stops != 1 || !active.stopping || g.Paused {
		t.Fatal("restart should stop the process while keeping the game enabled")
	}
	s.command("stop Example")
	if !g.Paused {
		t.Fatal("stop did not pause the game")
	}
	s.update(processUpdate{game: "example", process: miner, event: minerEvent{Done: true, Progress: []worker.ManagedProgress{{Login: "player", Status: "stopped"}}}}, now)
	s.update(processUpdate{game: "example", process: miner, exited: true}, now)
	s.tick(context.Background(), now)
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	persisted := saved.Games["example"]
	if !persisted.Paused || len(persisted.Campaigns[c.ID].Group) != 1 || c.Results["player"] != "" || s.active["example"] != nil {
		t.Fatal("pause or unfinished group was lost")
	}
	s.command("resume Example")
	saved, err = loadState(s.path)
	if err != nil || saved.Games["example"].Paused || s.state.Accounts[0].Game != "example" {
		t.Fatalf("resume did not retain the pool and clear the pause: %v", err)
	}
}
