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
	detailsUnavailable := false
	rawSingleOffline := false
	transport := reviewTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			OperationName string
			Variables     map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var data string
		switch request.OperationName {
		case "DropCampaignDetails":
			data = `{"user":{"dropCampaign":{"allow":{"isEnabled":true,"channels":[{"name":"live"},{"name":"offline"}]}}}}`
			if detailsUnavailable {
				data = `{"user":{"dropCampaign":null}}`
			}
		case "OrchestratorCampaignChannels":
			data = `{"user":{"dropCampaign":null}}`
			if rawSingleOffline {
				data = `{"user":{"dropCampaign":{"allow":{"isEnabled":true,"channels":[{"name":"offline"}]}}}}`
			}
		case "GameSlug":
			data = `{"game":{"slug":"game"}}`
		case "DirectoryPage_Game":
			data = `{"game":{"streams":{"edges":[{"node":{"id":"2","game":{"id":"3","displayName":"Game"},"broadcaster":{"id":"1","login":"live"}}}]}}}`
			if request.Variables["includeCostreaming"] == false {
				data = `{"game":{"streams":{"edges":[]}}}`
			}
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
	streams, checked, total, _, err := client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 0 || checked != 2 || total != 2 {
		t.Fatalf("expected no eligible streams among two channels: %v %d/%d %v", streams, checked, total, err)
	}
	offeredCampaign = wantCampaign
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" {
		t.Fatalf("expected the eligible live channel: %v %v", streams, err)
	}
	detailsUnavailable = true
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" {
		t.Fatalf("expected the eligible channel from the directory: %v %v", streams, err)
	}
	offeredCampaign = "other"
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err == nil || len(streams) != 0 {
		t.Fatalf("fallback accepted a different campaign or hid missing details: %v %v", streams, err)
	}
	rawSingleOffline = true
	streams, checked, total, status, err := client.CampaignStreams(context.Background(), "Game", wantCampaign, 0)
	if err != nil || len(streams) != 0 || checked != 1 || total != 1 || !strings.Contains(status, "offline · офлайн") {
		t.Fatalf("expected a verified offline channel from fallback details: %v %d/%d %q %v", streams, checked, total, status, err)
	}
}
