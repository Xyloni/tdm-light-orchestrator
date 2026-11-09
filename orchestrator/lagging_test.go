package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestLaggingReportUsesOnlyFreshActiveMeasurements(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "c", Group: []string{"slow", "normal", "stale", "finished", "starting"}}
	s.state.Games["example"] = &gameState{Name: "Example", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.active["example"] = &activeGame{groupStarted: now.Add(-time.Hour), progress: []worker.ManagedProgress{
		{Login: "slow", Status: "farm", RateKnown: true, RateChecked: now, Rate: 0.6},
		{Login: "normal", Status: "farm", RateKnown: true, RateChecked: now, Rate: 0.9},
		{Login: "stale", Status: "farm", RateKnown: true, RateChecked: now.Add(-time.Hour), Rate: 0.2},
		{Login: "finished", Status: "idle"},
	}}
	report := s.laggingReport("example", now)
	if report.Measured != 2 || report.Gathering != 2 || len(report.Slow) != 1 || report.Share() != 50 || report.AverageDeficit() != 40 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if output := report.text(); !strings.Contains(output, "slow: темп 60.0%, отставание 40.0%") || strings.Contains(output, "stale:") {
		t.Fatalf("unexpected output: %s", output)
	}
}
