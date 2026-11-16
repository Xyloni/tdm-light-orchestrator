package orchestrator

import (
	"testing"
	"time"
)

func TestManagedLotPreservesOnlyItsCampaigns(t *testing.T) {
	s := testScheduler(t, time.Now())
	sea := &gameState{Name: "Sea of Thieves", AutoClaimDrops: true, Current: "sea-campaign"}
	other := &gameState{Name: "Other game", AutoClaimDrops: true, Current: "sea-campaign"}
	s.state.Games["sea of thieves"], s.state.Games["other game"] = sea, other
	stopped, otherStopped := false, false
	s.active["sea of thieves"] = &activeGame{process: &minerProcess{stop: func() { stopped = true }}}
	s.active["other game"] = &activeGame{process: &minerProcess{stop: func() { otherStopped = true }}}
	s.state.LotCatalog = map[string]lotCatalogCampaign{
		"sea-campaign": {Game: sea.Name, Rewards: []lotReward{{Campaign: "sea-campaign", ID: "reward"}}},
	}
	ref := "0123456789abcdef01234567"
	if err := s.saveLotBundleLocked(ref, []string{"sea-campaign"}); err != nil {
		t.Fatal(err)
	}
	if !stopped || otherStopped {
		t.Fatalf("unexpected restarts: lot=%t other=%t", stopped, otherStopped)
	}
	if s.configForCampaign(sea, "sea-campaign").AutoClaimDrops ||
		!s.configForCampaign(sea, "another-campaign").AutoClaimDrops ||
		!s.configForCampaign(other, "sea-campaign").AutoClaimDrops {
		t.Fatal("campaign claim policies should only change for the lots' exact game and campaign")
	}
}
