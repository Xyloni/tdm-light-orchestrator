package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestStatisticsDeduplicatesAfterReloadAndSeparatesGames(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	archived := &campaign{ID: "archived", Finished: "завершена"}
	s.state.Games["example"] = &gameState{Campaigns: map[string]*campaign{archived.ID: archived}}
	s.initializeStats(now)
	sample := func(first, current int, ready bool) []worker.ManagedProgress {
		return []worker.ManagedProgress{{UserID: "player-id", RewardProgress: []worker.ManagedRewardProgress{
			{ID: "reward-id", First: first, Minutes: current, Known: true, Ready: ready},
		}}}
	}
	stats := &s.state.Stats
	stats.recordProgress("example", "current", sample(12, 12, false), now)
	stats.recordProgress("example", "current", sample(12, 19, false), now)
	if !s.persist() {
		t.Fatal("could not persist stats")
	}
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	stats = &saved.Stats
	stats.recordProgress("example", "current", sample(21, 21, true), now)
	stats.recordProgress("example", "current", sample(21, 21, true), now)
	stats.recordProgress("example", "current", sample(1, 18, true), now)
	stats.recordProgress("other", "current", sample(0, 5, false), now)
	stats.recordCampaign("example", archived, now.Format(statsDayLayout))
	if got := stats.sum("example", ""); got.Minutes != 9 || got.Rewards != 1 || got.Campaigns != 1 {
		t.Fatalf("unexpected lifetime counters: %+v", got)
	}
	if got := stats.sum("", now.Format(statsDayLayout)); got.Minutes != 14 || got.Rewards != 1 || got.Campaigns != 0 {
		t.Fatalf("unexpected day totals: %+v", got)
	}
	s.state = saved
	if !strings.Contains(s.command("stats Example"), "9 аккаунто-мин") {
		t.Fatal("bot stats game filter did not work")
	}
}
