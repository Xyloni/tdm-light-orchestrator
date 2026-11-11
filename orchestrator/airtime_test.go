package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
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
	if s.active["example"] != nil || len(c.Group) != 1 || !strings.Contains(s.queueSummary("example", g, now), "Проверено по ID кампании: 5") {
		t.Fatal("waiting campaign started or lost its group or wait reason")
	}
}

func TestActiveGroupRefreshesStreamsAndSurvivesOneFailedCheck(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "drop", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Minutes: 30}
	s.state.Games["game"] = &gameState{Name: "Game", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	old := twitch.Stream{ChannelID: "1", BroadcastID: "old", GameID: "game"}
	current := twitch.Stream{ChannelID: "1", BroadcastID: "new", GameID: "game"}
	air := &campaignAirtime{campaignID: c.ID, at: now, next: now.Add(time.Minute), streams: []twitch.Stream{current}}
	s.airtimes = map[string]*campaignAirtime{"game": air}
	stopped := false
	p := &minerProcess{commands: make(chan minerCommand, 1), stop: func() { stopped = true }}
	s.active["game"] = &activeGame{process: p, groupStarted: now, streams: []twitch.Stream{old}}

	s.tick(context.Background(), now)
	if stopped || len(p.commands) != 1 {
		t.Fatal("changed stream list should reach the running group without stopping it")
	}
	cmd := <-p.commands
	if !cmd.UpdateStreams || len(cmd.Streams) != 1 || cmd.Streams[0] != current {
		t.Fatalf("unexpected stream update: %+v", cmd)
	}

	air.streams, air.failed, air.failures = nil, true, 1
	s.tick(context.Background(), now)
	if stopped {
		t.Fatal("one inconclusive check should preserve the running group")
	}
	air.failures = 2
	s.tick(context.Background(), now)
	if !stopped {
		t.Fatal("repeated verification failure should stop the group")
	}
}
