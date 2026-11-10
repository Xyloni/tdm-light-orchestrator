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

func TestCampaignStreamsRequiresCampaignIDOnAllowedLiveChannel(t *testing.T) {
	wantCampaign := "target"
	offeredCampaign := "other"
	transport := reviewTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			OperationName string
			Variables     map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var data string
		switch request.OperationName {
		case "DropCampaignDetails":
			data = `{"user":{"dropCampaign":{"allow":{"isEnabled":true,"channels":[{"name":"live"},{"name":"offline"}]}}}}`
		case "VideoPlayerStreamInfoOverlayChannel":
			if request.Variables["channel"] == "offline" {
				data = `{"user":{"stream":null}}`
			} else {
				data = `{"user":{"id":"1","login":"live","stream":{"id":"2","game":{"id":"3","displayName":"Game"}}}}`
			}
		case "DropsHighlightService_AvailableDrops":
			data = `{"channel":{"viewerDropCampaigns":[{"id":"` + offeredCampaign + `"}]}}`
		default:
			t.Fatalf("unexpected operation %s", request.OperationName)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":` + data + `}`))}, nil
	})
	client := NewGQLClient(&http.Client{Transport: transport}, config.Default(), &parser.Account{Login: "fixture"})
	streams, checked, total, err := client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 0 || checked != 2 || total != 2 {
		t.Fatalf("expected no eligible streams among two channels: %v %d/%d %v", streams, checked, total, err)
	}
	offeredCampaign = wantCampaign
	streams, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" {
		t.Fatalf("expected the eligible live channel: %v %v", streams, err)
	}
}
