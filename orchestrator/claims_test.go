package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestClaimSummaryShowsOnlyUnconfirmedRewards(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	sample := worker.ManagedProgress{UserID: "test", RewardProgress: []worker.ManagedRewardProgress{
		{ID: "spray", Earned: true, ClaimError: "аккаунт не привязан"},
	}}
	s.state.Stats.recordProgress("game", "campaign", []worker.ManagedProgress{sample}, now)
	if summary := s.claimSummary("game"); !strings.Contains(summary, "аккаунт не привязан") || s.claimSummary("other") != "" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	sample.RewardProgress[0].Claimed = true
	s.state.Stats.recordProgress("game", "campaign", []worker.ManagedProgress{sample}, now)
	if summary := s.claimSummary("game"); summary != "" {
		t.Fatalf("confirmed reward still shows an error: %q", summary)
	}
}
