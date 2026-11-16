package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestManagedLotCampaignsPreserveEarnedRewards(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.ManagedLots = map[string]managedLot{
		"lot": {Game: "Game", Rewards: []lotReward{{Campaign: "lot-campaign", ID: "reward"}}},
	}
	game := &gameState{Name: "GAME", AutoClaimDrops: true}
	if cfg := s.configForCampaign(game, "lot-campaign"); cfg.AutoClaimDrops || cfg.CampaignID != "lot-campaign" {
		t.Fatal("worker is allowed to auto-claim the lot reward")
	}
	if !s.configForCampaign(game, "other-campaign").AutoClaimDrops {
		t.Fatal("lot changed auto-claim for a different campaign")
	}
	if state := classifyManagedLot(s.state.ManagedLots["lot"], twitch.LotInventory{Drops: []twitch.Drop{
		{ID: "reward", CampaignID: "lot-campaign", IsClaimed: true},
	}}); state == "ready" {
		t.Fatal("Twitch claim alone was accepted as proof of buyer availability")
	}
}

func TestManagedLotAccountOwnership(t *testing.T) {
	s := testScheduler(t, time.Now())
	const line = "player@example.invalid:player:test:fixture:123:device"
	if err := s.uploadLotCredentials([]byte(line)); err != nil {
		t.Fatal(err)
	}
	const ref = "0123456789abcdef01234567"
	req := managedLotRequest{Operation: "preview", Lot: ref, Login: "player"}
	if account, status := s.managedLotAccount(req); status != "" || account.UserID != "123" || len(s.state.LotAccounts) != 0 {
		t.Fatalf("preview mutated lot ownership or failed, status=%s", status)
	}
	req.Operation = "inspect"
	credentials, status := s.managedLotAccount(req)
	if status != "" || len(s.state.LotAccounts) != 1 || len(s.state.LotCredentials) != 0 {
		t.Fatalf("enrollment status=%s", status)
	}
	if err := s.upload([]byte(line)); err == nil {
		t.Fatal("held account accepted by miner")
	}
	other := managedLotRequest{Operation: "inspect", Lot: "111111111111111111111111", Login: "player"}
	if _, status = s.managedLotAccount(other); status != "in_another_lot" {
		t.Fatalf("double allocation status=%s", status)
	}
	req.Operation = "release"
	if status = s.finishManagedLotAccount(req, credentials); status != "returned" {
		t.Fatalf("return status=%s", status)
	}
	if status = s.finishManagedLotAccount(req, credentials); status != "returned" || len(s.state.Accounts) != 1 || s.state.Accounts[0].Game != "" {
		t.Fatalf("repeat return status=%s, accounts=%d", status, len(s.state.Accounts))
	}
	if _, status = s.managedLotAccount(other); status != "" {
		t.Fatalf("reuse status=%s", status)
	}
	other.Operation = "sold"
	if status = s.finishManagedLotAccount(other, credentials); status != "sold" || len(s.state.Accounts) != 0 || len(s.state.LotAccounts) != 0 {
		t.Fatalf("sale status=%s, still retained=%d", status, len(s.state.LotAccounts))
	}
	req.Operation, req.AccountLine = "inspect", line
	if _, status = s.managedLotAccount(req); status != "sold" {
		t.Fatalf("sold account re-enrollment status=%s", status)
	}
}

func TestManagedLotCatalogKeepsExactRewardIDs(t *testing.T) {
	s := testScheduler(t, time.Now())
	first := "Test Game\none | First\nfirst | Same Name\nsecond | Same Name | benefit-2\nСрок | 2026-11-23T18:00:00+03:00 | https://example.invalid/first"
	second := "Test Game\ntwo | Second\nthird | Another\nСрок | 2026-11-22T18:00:00+03:00 | https://example.invalid/second"
	for _, input := range []string{first, second} {
		if err := s.saveManagedLotInput("lot-catalog", input); err != nil {
			t.Fatal(err)
		}
	}
	const ref = "0123456789abcdef01234567"
	if err := s.saveManagedLotInput("lot-bundle", ref+"\none second\ntwo"); err != nil {
		t.Fatal(err)
	}
	lot := s.state.ManagedLots[ref]
	if len(s.state.Games) != 0 || len(lot.Rewards) != 2 || lot.Rewards[0].ID != "second" || lot.SuggestedCutoff.Day() != 22 {
		t.Fatalf("wrong bundle: %+v", lot)
	}
	if status := classifyManagedLot(lot, twitch.LotInventory{ClaimedBenefits: map[string]bool{"benefit-2": true}}); status != "claim_unverified" {
		t.Fatalf("unmapped claimed history produced a definitive absence: %s", status)
	}
	drops := []twitch.Drop{
		{CampaignID: "one", ID: "second", Current: 60, Required: 60, ProgressKnown: true, DropInstanceID: "one"},
		{CampaignID: "two", ID: "third", Current: 60, Required: 60, ProgressKnown: true, DropInstanceID: "two"},
	}
	if status := classifyManagedLot(lot, twitch.LotInventory{Drops: drops}); status != "ready" {
		t.Fatalf("state=%s", status)
	}
	if status := classifyManagedLot(lot, twitch.LotInventory{Drops: drops[:1]}); status != "missing" {
		t.Fatalf("state=%s", status)
	}
	if err := s.saveManagedLotInput("lot-bundle", ref+"\none wrong"); err == nil || !strings.Contains(err.Error(), "wrong") {
		t.Fatalf("invalid reward accepted, error=%v", err)
	}
}
