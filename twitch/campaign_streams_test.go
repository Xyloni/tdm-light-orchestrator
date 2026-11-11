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
	listedOffline := false
	directoryOmitsGame := false
	pagedDirectory := false
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
			if listedOffline {
				data = `{"user":{"dropCampaign":{"allow":{"isEnabled":true,"channels":[{"name":"offline"}]}}}}`
			}
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
			if directoryOmitsGame {
				data = `{"game":{"streams":{"edges":[{"node":{"id":"2","broadcaster":{"id":"1","login":"live"}}}]}}}`
			}
			if !directoryOmitsGame && request.Variables["includeCostreaming"] == false {
				data = `{"game":{"streams":{"edges":[]}}}`
			}
			if pagedDirectory && request.Variables["cursor"] != "page-two" {
				data = `{"game":{"streams":{"pageInfo":{"hasNextPage":true},"edges":[{"cursor":"page-two","node":{"id":"22","game":{"id":"3","displayName":"Game"},"broadcaster":{"id":"11","login":"different"}}}]}}}`
			}
		case "VideoPlayerStreamInfoOverlayChannel":
			if request.Variables["channel"] == "offline" {
				data = `{"user":{"stream":null}}`
			} else {
				data = `{"user":{"id":"1","login":"live","stream":{"id":"2","game":{"id":"3","displayName":"Game"}}}}`
			}
		case "DropsHighlightService_AvailableDrops":
			data = `{"channel":{"viewerDropCampaigns":[{"id":"` + offeredCampaign + `"}]}}`
			if pagedDirectory && request.Variables["channelID"] == "11" {
				data = `{"channel":{"viewerDropCampaigns":[{"id":"other"}]}}`
			}
		default:
			t.Fatalf("unexpected operation %s", request.OperationName)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":` + data + `}`))}, nil
	})
	client := NewGQLClient(&http.Client{Transport: transport}, config.Default(), &parser.Account{Login: "fixture"})
	streams, checked, total, _, err := client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || len(streams) != 0 || checked != 2 || total != 2 {
		t.Fatalf("expected no eligible streams among two channels: %v %d/%d %v", streams, checked, total, err)
	}
	offeredCampaign = wantCampaign
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" {
		t.Fatalf("expected the eligible live channel: %v %v", streams, err)
	}
	detailsUnavailable = true
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" {
		t.Fatalf("expected the eligible channel from the directory: %v %v", streams, err)
	}
	offeredCampaign = "other"
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err == nil || len(streams) != 0 {
		t.Fatalf("fallback accepted a different campaign or hid missing details: %v %v", streams, err)
	}
	rawSingleOffline = true
	streams, checked, total, status, err := client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || len(streams) != 0 || checked != 1 || total != 1 || !strings.Contains(status, "offline · офлайн") {
		t.Fatalf("expected a verified offline channel from fallback details: %v %d/%d %q %v", streams, checked, total, status, err)
	}
	listedOffline, directoryOmitsGame, detailsUnavailable = true, true, false
	offeredCampaign = wantCampaign
	streams, checked, total, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || checked != 1 || total != 1 || len(streams) != 1 || streams[0].Login != "live" || streams[0].GameID != "3" {
		t.Fatalf("expected confirmed directory stream with hydrated metadata: %v %d/%d %v", streams, checked, total, err)
	}
	offeredCampaign = "other"
	streams, _, _, status, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, nil)
	if err != nil || len(streams) != 0 || !strings.Contains(status, "offline · офлайн") || !strings.Contains(status, "найдено каналов: 1") {
		t.Fatalf("expected an offline listed channel and no matching directory streams: %v %q %v", streams, status, err)
	}

	pagedDirectory = true
	offeredCampaign = wantCampaign
	directory := &CampaignDirectoryCursor{}
	streams, _, _, status, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, directory)
	if err != nil || len(streams) != 0 || directory.drops != "page-two" || !strings.Contains(status, "следующих страницах") {
		t.Fatalf("expected the next page to be scheduled: %v %+v %q %v", streams, directory, status, err)
	}
	streams, _, _, _, err = client.CampaignStreams(context.Background(), "Game", wantCampaign, 0, directory)
	if err != nil || len(streams) != 1 || streams[0].Login != "live" || directory.drops != "page-two" {
		t.Fatalf("expected to find and retain the page with the eligible stream: %v %+v %v", streams, directory, err)
	}
}
