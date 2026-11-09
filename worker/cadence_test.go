package worker

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestSeparateHLSBeaconAndProgressCadence(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	cfg.TargetGame = "Game"
	acc := &parser.Account{Login: "test", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	hls, beacons, polls := 0, 0, 0
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		status := 200
		switch r.URL.Host {
		case "gql.twitch.tv":
			var p struct {
				Operation string `json:"operationName"`
			}
			json.NewDecoder(r.Body).Decode(&p)
			switch p.Operation {
			case "GameSlug":
				body = `{"data":{"game":{"slug":"game"}}}`
			case "DirectoryPage_Game":
				body = `{"data":{"game":{"streams":{"edges":[{"node":{"id":"2","game":{"id":"3"},"broadcaster":{"id":"1","login":"live"}}}]}}}}`
			case "PlaybackAccessToken":
				body = `{"data":{"streamPlaybackAccessToken":{"value":"test","signature":"test"}}}`
			case "Inventory":
				polls++
				body = `{"data":{"currentUser":{"inventory":{"dropCampaignsInProgress":[{"id":"campaign","game":{"name":"Game"},"timeBasedDrops":[{"id":"drop","name":"Reward","requiredMinutesWatched":60,"self":{"currentMinutesWatched":60}}]}]}}}}`
			default:
				t.Fatalf("unexpected operation %s", p.Operation)
			}
		case "usher.ttvnw.net":
			body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://test.ttvnw.net/live.m3u8\n"
		case "test.ttvnw.net":
			if strings.HasSuffix(r.URL.Path, ".m3u8") {
				hls++
				body = "#EXTM3U\n#EXTINF:2,\nsegment.ts\n"
			}
		case "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case "spade.twitch.tv":
			beacons++
			status = 204
		default:
			t.Fatal("unexpected host")
		}
		reply := reviewReply(body)
		reply.StatusCode = status
		return reply, nil
	})
	w := &accountWorker{m: m, acc: acc, st: m.states[acc.Login], gql: twitch.NewGQLClient(m.http, cfg, acc)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := w.farmDropScheduled(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Name: "Reward", Required: 60}, watchIntervals{hls: 10 * time.Millisecond, beacon: 100 * time.Millisecond, progress: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if hls < 10 || beacons < 2 || beacons > 3 || polls != 1 || w.st.DropsReady != 1 {
		t.Fatalf("HLS=%d beacon=%d inventory=%d ready=%d", hls, beacons, polls, w.st.DropsReady)
	}
}

func TestSignalDeadlineSkipsMissedSlots(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got := signalDeadline(time.Time{}, base, base.Add(5*time.Second), 20*time.Second); !got.Equal(base.Add(20 * time.Second)) {
		t.Fatal(got)
	}
	if got := signalDeadline(base, base, base.Add(45*time.Second), 20*time.Second); !got.Equal(base.Add(60 * time.Second)) {
		t.Fatal(got)
	}
	// A minute event is not moved by intermediate HLS checks or overdue slots.
	if got := signalDeadline(base, base, base.Add(45*time.Second), time.Minute); !got.Equal(base.Add(time.Minute)) {
		t.Fatal(got)
	}
}
