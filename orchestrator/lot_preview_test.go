package orchestrator

import (
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestLotInventoryDistinguishesCampaignsAndClaimState(t *testing.T) {
	required := []lotReward{{Campaign: "one", ID: "same"}, {Campaign: "two", ID: "same"}, {Campaign: "two", ID: "pending"}}
	got := inspectLotInventory(required, []twitch.Drop{
		{CampaignID: "one", ID: "same", IsClaimed: true},
		{CampaignID: "two", ID: "pending", Required: 10, Current: 10, ProgressKnown: true},
	})
	if got.Claimed != 1 || len(got.Missing) != 1 || got.Missing[0].Campaign != "two" || len(got.Unclaimed) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
}
