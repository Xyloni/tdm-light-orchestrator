package orchestrator

import (
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestNotificationPreferencesAndDeduplication(t *testing.T) {
	s := testScheduler(t, time.Now())
	var messages []string
	s.notify = func(text string) { messages = append(messages, text) }
	a := &activeGame{}
	game := &gameState{Name: "Example"}
	progress := []worker.ManagedProgress{
		{Login: "one", Status: "error: auth"},
		{Login: "two", Status: "farm", RateKnown: true},
		{Login: "three", Status: "farm", RateKnown: true},
	}
	s.command("notifications auth on")
	s.command("notifications stalled on")
	s.observeGroupNotices(a, game, progress)
	s.observeGroupNotices(a, game, progress)
	if len(messages) != 2 {
		t.Fatalf("expected one notification of each kind, got %d", len(messages))
	}
	s.command("notifications all off")
	s.sendNotice("catalog", "hidden")
	s.sendNotice("auth", "hidden")
	if len(messages) != 2 {
		t.Fatal("master switch did not mute notifications")
	}
	s.command("notifications all on")
	s.sendNotice("auth", "enabled again")
	if len(messages) != 3 || s.noticeEnabled("reward") {
		t.Fatal("master switch did not preserve individual settings")
	}
	saved, err := loadState(s.path)
	if err != nil || !saved.NotificationOverrides["auth"] || saved.NotificationsMuted {
		t.Fatalf("preferences were not saved: %v", err)
	}
}
