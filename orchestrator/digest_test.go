package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestDailyDigestUsesPreviousDayAndDoesNotRepeatAfterReload(t *testing.T) {
	now := time.Date(2026, time.November, 9, 20, 0, 0, 0, time.Local)
	s := testScheduler(t, now)
	s.state.Bot = &botSettings{Platform: "telegram", Token: "test", OwnerID: "123"}
	day := now.AddDate(0, 0, -1).Format(statsDayLayout)
	s.state.Stats.credit(day, "game", totals{Minutes: 90, Rewards: 2})
	s.state.Stats.DailyRewards = map[string]map[string]int64{day: {"Reward": 2}}
	s.state.Stats.DailyQuarantine = map[string]map[string]bool{day: {"test_login": true}}
	s.state.Stats.credit(now.Format(statsDayLayout), "game", totals{Minutes: 99})
	s.state.Games["example"] = &gameState{Name: "Example", Campaigns: map[string]*campaign{"c": {Name: "Closing soon", Ends: now.Add(time.Hour)}}}
	var messages []string
	s.notify = func(message string) { messages = append(messages, message) }
	s.setDigest("20:00")
	s.sendDailyDigest(now.Add(-time.Minute))
	s.sendDailyDigest(now)
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	s.state = saved
	s.sendDailyDigest(now.Add(time.Minute))
	if len(messages) != 1 || !strings.Contains(messages[0], "90 аккаунто-мин") || !strings.Contains(messages[0], "Reward ×2") ||
		!strings.Contains(messages[0], "test_login") || !strings.Contains(messages[0], "Closing soon") {
		t.Fatalf("unexpected digest messages: %v", messages)
	}
}
