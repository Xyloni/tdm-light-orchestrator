package twitch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestLotCampaignByIDQueriesExactCampaignAndRewards(t *testing.T) {
	const id = "df576175-0f22-467e-8ba7-955603a16567"
	for _, tt := range []struct {
		name, campaignID string
	}{
		{"match", id},
		{"not returned", ""},
		{"wrong ID", "5ce10491-2607-4cfe-a3cd-30b36e8a76ac"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"data":{"currentUser":{"dropCampaign":null}}}`
			if tt.campaignID != "" {
				body = `{"data":{"currentUser":{"dropCampaign":{"id":"` + tt.campaignID + `","name":"Borderlands 4 x The Hunt","game":{"name":"Borderlands 4"},"timeBasedDrops":[{"id":"drop","name":"The Horns","benefitEdges":[{"benefit":{"id":"benefit"}}]}]}}}}`
			}
			client := NewGQLClient(&http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
				var request struct {
					OperationName, Query string
					Variables            map[string]string
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.OperationName != "TDMManagedLotCampaign" || request.Variables["id"] != id ||
					!strings.Contains(request.Query, "dropCampaign(id: $id)") || !strings.Contains(request.Query, "benefitEdges") {
					t.Fatalf("unexpected Twitch query: %+v", request)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, config.Default(), &parser.Account{Login: "player", UserID: "17"})
			campaign, err := client.LotCampaignByID(context.Background(), id)
			if tt.campaignID != id {
				if campaign != nil || (tt.campaignID == "" && err != nil) {
					t.Fatalf("unexpected result for missing or mismatched campaign: %+v, %v", campaign, err)
				}
				return
			}
			if err != nil || campaign == nil || campaign.ID != id || campaign.Game.Name != "Borderlands 4" ||
				len(campaign.TimeBasedDrops) != 1 || len(campaign.TimeBasedDrops[0].BenefitEdges) != 1 ||
				campaign.TimeBasedDrops[0].BenefitEdges[0].Benefit.ID != "benefit" {
				t.Fatalf("unexpected campaign: %+v, %v", campaign, err)
			}
		})
	}
}

func TestLotCampaignInventoryImportsExactIDs(t *testing.T) {
	for _, user := range []string{"player", "another"} {
		body := `{"data":{"currentUser":{"id":"17","login":"` + user + `","inventory":{"dropCampaignsInProgress":[{"id":"campaign","name":"Old season","game":{"name":"Game"},"timeBasedDrops":[{"id":"drop","name":"Reward","benefitEdges":[{"benefit":{"id":"benefit"}}]}]}]}}}}`
		client := NewGQLClient(&http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, config.Default(), &parser.Account{Login: "player", UserID: "17"})
		catalog, err := client.LotCampaignsInInventory(context.Background())
		if user != "player" {
			if err == nil {
				t.Fatal("different user accepted")
			}
			continue
		}
		if err != nil || len(catalog) != 1 || catalog[0].TimeBasedDrops[0].BenefitEdges[0].Benefit.ID != "benefit" {
			t.Fatalf("catalog=%+v error=%v", catalog, err)
		}
	}
}

func TestReadLotInventoryRequiresConfirmedAbsence(t *testing.T) {
	for _, tt := range []struct {
		name, userID, inventory string
		empty, fail             bool
	}{
		{"empty", "17", `{"gameEventDrops":[],"dropCampaignsInProgress":null}`, true, false},
		{"identity mismatch", "18", `{"gameEventDrops":[],"dropCampaignsInProgress":[]}`, false, true},
		{"missing inventory field", "17", `{"dropCampaignsInProgress":[]}`, false, true},
		{"missing progress list", "17", `{"gameEventDrops":[]}`, false, true},
		{"claimed", "17", `{"gameEventDrops":[{"id":"benefit"}],"dropCampaignsInProgress":[]}`, false, false},
		{"unknown progress", "17", `{"gameEventDrops":[],"dropCampaignsInProgress":[{"id":"campaign","timeBasedDrops":[{"id":"drop","self":null}]}]}`, false, false},
		{"unclaimed", "17", `{"gameEventDrops":[],"dropCampaignsInProgress":[{"id":"campaign","timeBasedDrops":[{"id":"drop","requiredMinutesWatched":1,"self":{"currentMinutesWatched":1,"isClaimed":false,"dropInstanceID":"instance"}}]}]}`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"data":{"currentUser":{"id":"` + tt.userID + `","login":"player","inventory":` + tt.inventory + `}}}`
			client := NewGQLClient(&http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, config.Default(), &parser.Account{Login: "player", UserID: "17"})
			inv, err := client.ReadLotInventory(context.Background())
			if (err != nil) != tt.fail || inv.Empty != tt.empty {
				t.Fatalf("empty=%t, err=%v", inv.Empty, err)
			}
		})
	}
}
