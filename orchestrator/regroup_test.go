package orchestrator

import (
	"net/url"
	"testing"
	"time"
)

func TestRegroupSelectedGameWaitsForExitAndPreservesResults(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	a := &campaign{ID: "a", Group: []string{"waiting"}, Results: map[string]string{"done": "idle", "trimmed": "trimmed"}}
	b := &campaign{ID: "b", Group: []string{"other"}}
	s.state.Games["first"] = &gameState{Current: a.ID, Campaigns: map[string]*campaign{a.ID: a}}
	s.state.Games["second"] = &gameState{Current: b.ID, Campaigns: map[string]*campaign{b.ID: b}}
	stops := 0
	first := &minerProcess{stop: func() { stops++ }}
	s.active["first"] = &activeGame{process: first}
	s.active["second"] = &activeGame{process: &minerProcess{stop: func() { t.Fatal("unselected miner stopped") }}}
	form := url.Values{"action": {"regroup"}, "scope": {"game:first"}}
	s.panelAction(form)
	if stops != 0 || s.state.Games["first"].Regroup {
		t.Fatal("regroup without confirmation")
	}
	form.Set("confirm", "on")
	s.panelAction(form)
	s.finishRegroups()
	if stops != 1 || len(a.Group) != 1 || len(b.Group) != 1 {
		t.Fatal("group reset before miner exit")
	}
	s.update(processUpdate{game: "first", process: first, exited: true}, now)
	s.finishRegroups()
	state, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	game := state.Games["first"]
	c := game.Campaigns[a.ID]
	if game.Regroup || len(c.Group) != 0 || c.Results["done"] != "idle" || c.Results["trimmed"] != "trimmed" ||
		len(state.Games["second"].Campaigns[b.ID].Group) != 1 {
		t.Fatal("regroup did not reset only the selected plan while preserving results")
	}
}
