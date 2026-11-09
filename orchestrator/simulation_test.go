package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestSimulationKeepsSlotThroughGroupsAndReportsMisses(t *testing.T) {
	jobs := []simulationJob{
		{Name: "A", Accounts: 30, Duration: 30 * time.Minute, Deadline: 90 * time.Minute, Economy: true},
		{Name: "B", Accounts: 10, Duration: 45 * time.Minute, Deadline: 120 * time.Minute, Economy: true},
	}
	result, peak, ok := simulateSchedule(jobs, 1, 20)
	if !ok || peak != 10 || len(result[0].Groups) != 3 || result[0].Groups[0] != 10 || result[1].Started != 90*time.Minute || result[1].Missed != 10 {
		t.Fatalf("unexpected simulation: %+v peak=%d done=%t", result, peak, ok)
	}
}

func TestSchedulePreviewUsesHypotheticalModeWithoutChangingLiveState(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	c := &campaign{ID: "campaign", Starts: now.Add(-time.Hour), Ends: now.Add(90 * time.Minute), Minutes: 15, Group: []string{"a"}}
	g := &gameState{Name: "Example", Target: 3, Mode: "speed", Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	s.state.Games["example"] = g
	for _, login := range []string{"a", "b", "c"} {
		s.state.Accounts = append(s.state.Accounts, account{Game: "example", Credentials: parser.Account{Login: login}})
	}
	preview := s.previewSchedule("game:example", "resources", now)
	if !strings.Contains(preview, "3×1") || g.Mode != "speed" || len(c.Group) != 1 || c.Group[0] != "a" || len(s.active) != 0 {
		t.Fatalf("preview was incorrect or changed live state: %s", preview)
	}
}
