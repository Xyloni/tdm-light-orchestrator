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
		{Login: "slow", Status: "farm", RateKnown: true, RateChecked: now, Rate: 0.6, LongRateKnown: true, LongRateChecked: now, LongRate: 0.8999},
		{Login: "normal", Status: "farm", RateKnown: true, RateChecked: now, Rate: 0.9, LongRateKnown: true, LongRateChecked: now, LongRate: 0.8},
		{Login: "stale", Status: "farm", RateKnown: true, RateChecked: now.Add(-time.Hour), Rate: 0.2, LongRateKnown: true, LongRateChecked: now.Add(-time.Hour), LongRate: 0.2},
		{Login: "finished", Status: "idle"},
	}}
	report := s.laggingReport("example", now)
	if report.Measured != 2 || report.Gathering != 2 || report.ShortLow != 1 || report.LongMeasured != 2 || report.LongGathering != 2 || report.LongLow != 2 || report.BothLow != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if output := report.details(10); !strings.Contains(output, "10–15 мин: 60.0% · 30–35 мин: чуть ниже 90%") || strings.Contains(output, "stale") {
		t.Fatalf("unexpected output: %s", output)
	}
}
