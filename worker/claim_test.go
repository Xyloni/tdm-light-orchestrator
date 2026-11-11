package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestAutoClaimRequiresInventoryConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled, confirms bool
		wantClaims        int
	}{{"disabled", false, false, 0}, {"confirmed", true, true, 1}, {"unconfirmed", true, false, maxClaimAttempts}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.AutoClaimDrops, cfg.TargetGame, cfg.CampaignID, cfg.ManagedAuthAttempts = tc.enabled, "Game", "campaign", 1
			acc := &parser.Account{Login: "fixture"}
			m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
			state := m.states[acc.Login]
			state.trackStats = true
			claims := 0
			m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
				var req struct {
					OperationName string
					Variables     struct {
						Input struct{ DropInstanceID string }
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req.OperationName == "DropsPage_ClaimDropRewards" {
					claims++
					if req.Variables.Input.DropInstanceID != "own-instance" {
						t.Fatal("claim used an unexpected drop instance")
					}
					return reviewReply(`{"data":{"claimDropRewards":{"status":"ELIGIBLE_FOR_ALL"}}}`), nil
				}
				claimed := "false"
				if tc.confirms && claims > 0 {
					claimed = "true"
				}
				inventory := strings.Replace(completedGroupInventory, `"isClaimed":false`, `"isClaimed":`+claimed+`,"dropInstanceID":"own-instance"`, 1)
				// Keep only the first drop in this fixture.
				inventory = strings.Split(inventory, `,{"id":"second"`)[0] + `]}]}}}}`
				return reviewReply(inventory), nil
			})
			w := &accountWorker{m: m, acc: acc, st: state, gql: twitch.NewGQLClient(m.http, cfg, acc)}
			for i := 0; i < maxClaimAttempts; i++ {
				_, _, _ = w.nextDrop(context.Background())
			}
			if claims != tc.wantClaims || state.rewardProgress["first"].Claimed != tc.confirms {
				t.Fatalf("unexpected result: claims=%d, progress=%+v", claims, state.rewardProgress["first"])
			}
		})
	}
}
