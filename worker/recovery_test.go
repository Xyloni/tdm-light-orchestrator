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

func TestFarmDropSwitchesChannelWhileBeaconStillWorks(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	acc := &parser.Account{Login: "test", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	directories, goodHeartbeats := 0, 0
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
				directories++
				nodes := `{"node":{"id":"2","game":{"id":"3"},"broadcaster":{"id":"1","login":"bad"}}}`
				if directories > 1 {
					nodes += `,{"node":{"id":"2","game":{"id":"3"},"broadcaster":{"id":"1","login":"good"}}}`
				}
				body = `{"data":{"game":{"streams":{"edges":[` + nodes + `]}}}}`
			case "PlaybackAccessToken":
				body = `{"data":{"streamPlaybackAccessToken":{"value":"test","signature":"test"}}}`
			default:
				t.Fatalf("unexpected GQL operation %q", p.Operation)
			}
		case "usher.ttvnw.net":
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/channel/hls/"), ".m3u8")
			body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://test.ttvnw.net/" + name + ".m3u8\n"
		case "test.ttvnw.net":
			if strings.Contains(r.URL.Path, "bad") {
				status = 404
			} else {
				goodHeartbeats++
				cancel()
				return nil, context.Canceled
			}
		case "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case "spade.twitch.tv":
			status = 204
		default:
			t.Fatal("unexpected host")
		}
		response := reviewReply(body)
		response.StatusCode = status
		return response, nil
	})
	w := &accountWorker{m: m, acc: acc, st: m.states[acc.Login], gql: twitch.NewGQLClient(m.http, cfg, acc)}
	err := w.farmDrop(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Name: "Reward", Required: 60})
	if err != nil {
		t.Fatal(err)
	}
	if directories != 2 || goodHeartbeats != 1 || !time.Now().Before(w.unavailable["bad"]) {
		t.Fatalf("directories=%d good=%d cooldown=%v", directories, goodHeartbeats, w.unavailable)
	}
}

func TestProgressVisibleWhenUnchangedAndUnknown(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	acc := &parser.Account{Login: "test"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	w := &accountWorker{m: m, st: m.states[acc.Login]}
	d := twitch.Drop{ID: "drop", Name: "Reward", Current: 13, Required: 60, ProgressKnown: true}
	w.reportProgress(d)
	w.reportProgress(d)
	if got := m.logsText("test"); !strings.Contains(got, "13/60 мин") || strings.Count(got, "прогресс «Reward»") != 2 {
		t.Fatalf("progress not visible: %s", got)
	}
	d.ProgressKnown = false
	w.reportProgress(d)
	if got := m.logsText("test"); !strings.Contains(got, "личный прогресс пока не получен") {
		t.Fatal(got)
	}
}
