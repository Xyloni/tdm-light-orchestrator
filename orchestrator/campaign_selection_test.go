package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestManualCampaignSwitchPreservesPreviousGroup(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	first := &campaign{ID: "first", Name: "Playoffs", Starts: now.Add(-time.Hour), Ends: now.Add(time.Hour), Group: []string{"player"}, Results: map[string]string{"done": "idle"}}
	second := &campaign{ID: "second", Name: "Midseason", Starts: now.Add(-time.Hour), Ends: now.Add(2 * time.Hour)}
	g := &gameState{Name: "Overwatch", Current: first.ID, Campaigns: map[string]*campaign{first.ID: first, second.ID: second}}
	s.state.Games["overwatch"] = g
	stopped := false
	process := &minerProcess{stop: func() { stopped = true }}
	s.active["overwatch"] = &activeGame{process: process}
	action := "farm-pick:" + gameRef("overwatch") + ":" + gameRef(second.ID)
	panel := s.telegramMenu(action)
	if len("control:"+action) > 64 || !strings.Contains(panel.Text, second.Name) || !stopped || g.Current != first.ID {
		t.Fatalf("unexpected switch result: %q", panel.Text)
	}
	if s.queuedCampaign(g, now) != second || len(first.Group) != 1 || first.Results["done"] != "idle" {
		t.Fatal("selection did not preserve the previous group's progress")
	}
	s.update(processUpdate{game: "overwatch", process: process, exited: true}, now)
	second.Finished = "завершена"
	if s.queuedCampaign(g, now) != nil {
		t.Fatal("manual mode started a different campaign")
	}
	s.telegramMenu("farm-pick:" + gameRef("overwatch") + ":auto")
	if s.queuedCampaign(g, now) != first {
		t.Fatal("auto mode did not resume the previous campaign")
	}
}
