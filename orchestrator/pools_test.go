package orchestrator

import (
	"net/url"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestClearSelectedPool(t *testing.T) {
	s := testScheduler(t, time.Now())
	c := &campaign{ID: "campaign", Group: []string{"player"}, Results: map[string]string{"done": "idle"}}
	g := &gameState{Name: "Example", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	s.state.Accounts = []account{
		{Credentials: parser.Account{Login: "player"}, Game: "example"},
		{Credentials: parser.Account{Login: "other"}, Game: "different"},
		{Credentials: parser.Account{Login: "free"}},
	}
	form := url.Values{"action": {"clear-pool"}, "scope": {"game:example"}}
	s.panelAction(form)
	if s.state.Accounts[0].Game != "example" {
		t.Fatal("cleared without confirmation")
	}
	form.Set("confirm", "on")
	form.Set("disposition", "release")
	s.active["example"] = &activeGame{}
	s.panelAction(form)
	if s.state.Accounts[0].Game != "example" {
		t.Fatal("cleared while miner was active")
	}
	delete(s.active, "example")
	s.panelAction(form)
	if !g.Paused || s.state.Accounts[0].Game != "" || s.state.Accounts[1].Game != "different" || len(c.Group) != 0 || c.Results["done"] != "idle" {
		t.Fatal("clear game must release only its accounts and retain campaign results")
	}
	form.Set("scope", "free")
	form.Set("disposition", "delete")
	s.panelAction(form)
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Accounts) != 1 || saved.Accounts[0].Game != "different" || !saved.Games["example"].Paused {
		t.Fatal("free pool clear did not preserve the other game pool")
	}
}
