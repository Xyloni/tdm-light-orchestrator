package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestCampaignWaitsWithoutAnEligibleStream(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "drop", Name: "Event", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Minutes: 30, Group: []string{"player"}}
	g := &gameState{Name: "Example", Target: 1, Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	s.state.Accounts = []account{{Game: "example", Credentials: parser.Account{Login: "player"}}}
	s.airtimes = map[string]*campaignAirtime{"example": {campaignID: c.ID, at: now, next: now.Add(time.Minute), checked: 5, total: 5}}
	stopped := false
	process := &minerProcess{stop: func() { stopped = true }}
	s.active["example"] = &activeGame{process: process, groupStarted: now}
	s.tick(context.Background(), now)
	if !stopped {
		t.Fatal("miner kept running without an eligible stream")
	}
	s.update(processUpdate{game: "example", process: process, exited: true}, now)
	s.tick(context.Background(), now)
	if s.active["example"] != nil || len(c.Group) != 1 || !strings.Contains(s.queueSummary("example", g, now), "0 из 5") {
		t.Fatal("waiting campaign started or lost its group or wait reason")
	}
}
