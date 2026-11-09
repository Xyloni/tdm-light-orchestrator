package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestRewardExclusionAppliesOnlyToSelectedGame(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "current", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Minutes: 60,
		Rewards: []rewardRequirement{{Name: "Royal Purple Banana", Minutes: 60}},
		Group:   []string{"working"}, Results: map[string]string{"finished": "idle", "broken": "error: auth"}}
	g := &gameState{Name: "Example", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	s.state.Games["other"] = &gameState{Name: "Other"}
	stopped := false
	p := &minerProcess{stop: func() { stopped = true }}
	s.active["example"] = &activeGame{process: p}
	s.panelAction(url.Values{"action": {"exclude"}, "game": {"example"}, "reward": {"royal purple banana"}})
	other := s.configForGame(s.state.Games["other"])
	if !stopped || c.Minutes != 0 || campaignReady(c, now) || other.DropIgnored("Royal Purple Banana") {
		t.Fatal("exclusion should apply to the selected game and stop its outdated miner")
	}
	handler, err := webHandler(s)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	if !strings.Contains(response.Body.String(), `value="royal purple banana"`) || !strings.Contains(response.Body.String(), "Вернуть в фарм") {
		t.Fatal("exclusion is missing from the game card")
	}
	s.command("include Example | Royal Purple Banana")
	s.update(processUpdate{game: "example", process: p, event: minerEvent{Done: true, Progress: []worker.ManagedProgress{{Login: "working", Status: "idle"}}}}, now)
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	updated := saved.Games["example"].Campaigns[c.ID]
	if updated.Minutes != 60 || !campaignReady(updated, now) || updated.Results["finished"] != "" ||
		updated.Results["working"] != "" || updated.Results["broken"] != "error: auth" {
		t.Fatal("restoring a reward should recheck completed accounts and ignore the old miner result")
	}
}
