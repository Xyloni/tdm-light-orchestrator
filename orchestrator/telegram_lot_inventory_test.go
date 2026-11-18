package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestTelegramLotInventoryOffersCampaignsFromFirstAuthorizedAccount(t *testing.T) {
	s := testScheduler(t, time.Now())
	const ref = "0123456789abcdef01234567"
	addCatalogFixture(t, s, "Borderlands 4", "decoy", "Catalog-only campaign")
	s.state.LotCredentials = map[string]parser.Account{
		"player_first": {Login: "player_first", OAuth: "first-fixture", UserID: "21"},
	}
	sample := []lotSampleAccount{
		{Login: "login_only"},
		{Login: "player_first"},
		{Login: "player_second", OAuth: "second-fixture", UserID: "22", DeviceID: "device"},
	}
	if err := s.registerLotTitle(ref, "Borderlands 4 lot", sample); err != nil {
		t.Fatal(err)
	}

	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var inventoryRequests, detailsRequests int
	http.DefaultTransport = managedLotDiagnosticTransport(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "OAuth first-fixture" {
			t.Errorf("inventory used the wrong sample account: %q", got)
		}
		var request struct {
			OperationName string
			Variables     map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		var body string
		switch request.OperationName {
		case "TDMManagedLotInventoryCatalog":
			inventoryRequests++
			body = `{"data":{"currentUser":{"id":"21","login":"player_first","inventory":{"dropCampaignsInProgress":[
				{"id":"second-game-campaign","name":"Festival","game":{"name":"Another Game"},"timeBasedDrops":[{"id":"festival-reward","name":"Festival Reward"}]}
			]}}}}`
		case "TDMManagedLotClaimedCampaigns":
			body = `{"data":{"currentUser":{"id":"21","login":"player_first","inventory":{"earnedDropRewards":{"edges":[
				{"node":{"status":"CLAIMED","campaign":{"id":"hunt"}}},
				{"node":{"status":"CLAIMED","campaign":{"id":"hunt"}}},
				{"node":{"status":"UNCLAIMED","campaign":{"id":"decoy"}}}
			]}}}}}`
		case "TDMManagedLotCampaign":
			detailsRequests++
			if request.Variables["id"] != "hunt" {
				t.Errorf("looked up the wrong campaign: %+v", request.Variables)
			}
			body = `{"data":{"currentUser":{"dropCampaign":{"id":"hunt","name":"The Hunt","game":{"name":"Borderlands 4"},"timeBasedDrops":[
				{"id":"horns","name":"The Horns","benefitEdges":[{"benefit":{"id":"horns-benefit"}}]},
				{"id":"other-reward","name":"Another Reward"}]
			}}}}`
		default:
			t.Errorf("unexpected Twitch operation %q", request.OperationName)
			return nil, fmt.Errorf("unexpected Twitch operation %s", request.OperationName)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	editor := s.telegramMenu("lb-start:" + ref)
	results := s.telegramMenu(catalogAction(t, editor, "Из инвентаря лота"))
	if results.Input != "" || inventoryRequests != 1 || detailsRequests != 1 || len(s.state.ManagedLots) != 0 || len(s.state.LotCatalog) != 1 {
		t.Fatalf("inventory scan asked for text, repeated the scan or saved the lot: %+v", results)
	}
	for _, row := range results.Buttons {
		for _, button := range row {
			if strings.Contains(button.Label, "Catalog-only campaign") {
				t.Fatal("inventory results included a campaign absent from the account")
			}
		}
	}
	catalogAction(t, results, "Another Game · Festival")
	rewards := s.telegramMenu(catalogAction(t, results, "Borderlands 4 · The Hunt"))
	rewards = s.telegramMenu(catalogAction(t, rewards, "The Horns"))
	review := s.telegramMenu(catalogAction(t, rewards, "Просмотреть и сохранить"))
	saved := s.telegramMenu(catalogAction(t, review, "Сохранить состав"))
	if !strings.Contains(saved.Text, "Состав сохранён") {
		t.Fatalf("could not save the selected inventory reward: %+v", saved)
	}
	lot := s.state.ManagedLots[ref]
	if lot.Game != "Borderlands 4" || len(lot.Rewards) != 1 || lot.Rewards[0].Campaign != "hunt" || lot.Rewards[0].ID != "horns" {
		t.Fatalf("saved the wrong inventory data: %+v", lot)
	}
	if benefits := lot.Benefits["hunt/horns"]; len(benefits) != 1 || benefits[0] != "horns-benefit" {
		t.Fatalf("did not save the reward's benefit mapping: %+v", benefits)
	}
	if len(s.state.LotCatalog) != 2 || len(s.state.LotCatalog["hunt"].Rewards) != 2 {
		t.Fatal("only the selected campaign should be added to the persistent catalog")
	}
}
