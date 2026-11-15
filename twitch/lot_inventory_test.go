package twitch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

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
