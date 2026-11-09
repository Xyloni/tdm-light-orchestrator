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

func TestInventoryOverridesStaleCatalogProgress(t *testing.T) {
	calls := 0
	client := NewGQLClient(&http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var payload struct {
			Operation string `json:"operationName"`
			Query     string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Operation != "Inventory" || !strings.Contains(payload.Query, "dropCampaignsInProgress") {
			t.Fatal("wrong progress source")
		}
		body := `{"data":{"currentUser":{"dropCampaigns":[{"id":"campaign","name":"Campaign","status":"ACTIVE","game":{"name":"Game"},"timeBasedDrops":[{"id":"drop","name":"Reward","requiredMinutesWatched":60,"self":{"currentMinutesWatched":0,"isClaimed":false}}]}],"inventory":{"dropCampaignsInProgress":[{"id":"campaign","timeBasedDrops":[{"id":"drop","self":{"currentMinutesWatched":13,"isClaimed":false,"dropInstanceID":"instance"}}]}]}}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, config.Default(), &parser.Account{})
	drops, err := client.DropsDashboard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(drops) != 1 {
		t.Fatalf("calls=%d drops=%d", calls, len(drops))
	}
	d := drops[0]
	if !d.ProgressKnown || d.Current != 13 || d.Required != 60 || d.Name != "Reward" || d.GameName != "Game" || d.CampaignStatus != "ACTIVE" {
		t.Fatalf("incorrect merge: %+v", d)
	}
}

func TestInventoryMissingProgressIsNotZero(t *testing.T) {
	for _, inventory := range []string{`null`, `{"dropCampaignsInProgress":[]}`, `{"dropCampaignsInProgress":[{"id":"campaign","timeBasedDrops":[{"id":"drop","self":null}]}]}`, `{"dropCampaignsInProgress":[{"id":"campaign","timeBasedDrops":[{"id":"drop","self":{}}]}]}`} {
		t.Run(inventory, func(t *testing.T) {
			body := `{"data":{"currentUser":{"dropCampaigns":[{"id":"campaign","timeBasedDrops":[{"id":"drop","requiredMinutesWatched":60,"self":{"currentMinutesWatched":59}}]}],"inventory":` + inventory + `}}}`
			c := NewGQLClient(&http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, config.Default(), &parser.Account{})
			drops, err := c.DropsDashboard(context.Background())
			if inventory == "null" {
				if err == nil {
					t.Fatal("missing inventory accepted")
				}
				return
			}
			if err != nil || len(drops) != 1 {
				t.Fatalf("drops=%v error=%v", drops, err)
			}
			if drops[0].ProgressKnown {
				t.Fatal("unavailable personal progress reported as confirmed")
			}
		})
	}
}

func TestHeartbeatRequestsChannelChangeAfterFailedRefresh(t *testing.T) {
	refreshes := 0
	h := &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		status, body := 200, ""
		switch r.URL.Host {
		case "gql.twitch.tv":
			refreshes++
			body = `{"data":{"streamPlaybackAccessToken":{"value":"test","signature":"test"}}}`
		case "test.ttvnw.net", "usher.ttvnw.net":
			status = 404
		case "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case "spade.twitch.tv":
			status = 204
		default:
			t.Fatal("unexpected host")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	c := NewGQLClient(h, config.Default(), &parser.Account{UserID: "1"})
	result, err := c.WatchHeartbeat(context.Background(), "https://test.ttvnw.net/live.m3u8", Stream{Login: "offline", ChannelID: "1", BroadcastID: "2", GameID: "3"})
	if err != nil || !result.HLSFailed || result.Method != "minute-watched beacon" || refreshes != 1 {
		t.Fatalf("result=%+v refreshes=%d error=%v", result, refreshes, err)
	}
}

func TestHeartbeatCancellationDoesNotSendBeacon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := NewGQLClient(&http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) { calls++; cancel(); return nil, context.Canceled })}, config.Default(), &parser.Account{})
	_, err := c.WatchHeartbeat(ctx, "https://test.ttvnw.net/live.m3u8", Stream{})
	if err != context.Canceled || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
