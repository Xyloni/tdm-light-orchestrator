package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestDeadlineReportAndCompletedExport(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "event", Name: "Event", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Minutes: 60,
		Results: map[string]string{"done": "idle"}, Group: []string{"fast", "slow"}}
	g := &gameState{Name: "Game", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["game"] = g
	for _, login := range []string{"done", "fast", "slow"} {
		s.state.Accounts = append(s.state.Accounts, account{Game: "game", Credentials: parser.Account{Login: login}})
	}
	s.airtimes = map[string]*campaignAirtime{"game": {campaignID: c.ID, streams: []twitch.Stream{{Login: "stream"}}}}
	s.active["game"] = &activeGame{groupStarted: now.Add(-time.Minute), progress: []worker.ManagedProgress{
		{Login: "fast", Current: 30, Required: 60, RateKnown: true, Rate: 1, RateChecked: now},
		{Login: "slow", Current: 5, Required: 60, RateKnown: true, Rate: .5, RateChecked: now},
	}}
	if got := s.deadlineForecast("game", g, c, now); got != (deadlineReport{total: 3, done: 1, onTrack: 1, atRisk: 1}) {
		t.Fatalf("unexpected forecast: %+v", got)
	}
	var notices []string
	s.notify = func(text string) { notices = append(notices, text) }
	s.sendDeadlineReports(now)
	s.sendDeadlineReports(now.Add(time.Minute))
	if len(notices) != 1 || !strings.Contains(notices[0], "Под риском: 1") {
		t.Fatalf("unexpected notices: %v", notices)
	}
	panel := s.telegramMenu("deadline-game:" + gameRef("game") + ":" + gameRef(c.ID))
	export := s.telegramMenu(panel.Buttons[0][0].Action)
	doc := s.telegramMenu(export.Buttons[0][0].Action).Document
	if doc == nil || !strings.Contains(string(doc.Data), ":done:") || strings.Contains(string(doc.Data), ":fast:") {
		t.Fatal("deadline export did not include only completed accounts")
	}
}

func TestRestartRequiresThreeConfirmationsAndRenderedResponse(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	s.restartRequests = make(chan struct{}, 1)
	s.telegramRestart("restart-all", now)
	if skipped := s.telegramRestart("restart-all-final-confirm", now); skipped.AfterRender != nil {
		t.Fatal("restart skipped confirmations")
	}
	for _, action := range []string{"restart-all-first-confirm", "restart-all-second-confirm"} {
		if panel := s.telegramRestart(action, now); panel.AfterRender != nil {
			t.Fatal("restart queued before final confirmation")
		}
	}
	panel := s.telegramRestart("restart-all-final-confirm", now)
	if panel.AfterRender == nil || len(s.restartRequests) != 0 {
		t.Fatal("restart should be queued after confirmation is rendered")
	}
	panel.AfterRender()
	if len(s.restartRequests) != 1 {
		t.Fatal("restart was not requested")
	}
}
