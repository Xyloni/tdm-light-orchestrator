package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestActiveGroupETAUsesRemainingProgress(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "selected", Ends: now.Add(time.Hour), Group: []string{"done", "active"}}
	s.airtimes = map[string]*campaignAirtime{"game": {campaignID: c.ID, streams: []twitch.Stream{{Login: "live"}}}}
	a := &activeGame{groupStarted: now.Add(-time.Hour), progress: []worker.ManagedProgress{
		{Login: "done", Status: "idle"},
		{Login: "active", Required: 100, Current: 70},
	}}
	s.active["game"] = a
	if got := s.activeGroupETA("game", c, now); !strings.Contains(got, "0 из 1") {
		t.Fatalf("expected an unknown estimate, got %q", got)
	}
	a.progress[1].ProgressKnown = true
	a.progress[1].RateKnown = true
	a.progress[1].Rate = 1
	a.progress[1].RateChecked = now
	want := now.Add(30 * time.Minute).Local().Format("02.01 в 15:04 MST")
	if got := s.activeGroupETA("game", c, now); !strings.Contains(got, want) {
		t.Fatalf("expected %s, got %q", want, got)
	}
}
