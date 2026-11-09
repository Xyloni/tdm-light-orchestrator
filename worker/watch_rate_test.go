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

func TestWatchRateDetectsSlowPositiveProgress(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		values []int
		slow   bool
	}{
		{"slow but positive", []int{20, 21, 22, 23, 24, 25}, true},
		{"stalled", []int{20, 20, 20, 20, 20, 20}, true},
		{"normal", []int{20, 22, 24, 26, 28, 30}, false},
		{"late catchup", []int{20, 20, 21, 22, 28, 30}, false},
		{"rounding allowance", []int{20, 21, 23, 25, 27, 27}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r watchRate
			r.reset(base, tc.values[0], true)
			for i := 1; i < len(tc.values); i++ {
				slow, _, _ := r.observe(base.Add(time.Duration(2*i)*time.Minute), tc.values[i], true, 5*time.Minute)
				if i < 5 && slow {
					t.Fatal("recovery before full window")
				}
				if i == 5 && slow != tc.slow {
					t.Fatalf("slow=%v want=%v", slow, tc.slow)
				}
			}
		})
	}
}

func TestWatchRateResetsInvalidObservations(t *testing.T) {
	base := time.Now()
	for _, kind := range []string{"unknown", "gap", "regression", "clock"} {
		t.Run(kind, func(t *testing.T) {
			var r watchRate
			r.reset(base, 20, true)
			for i := 1; i <= 4; i++ {
				r.observe(base.Add(time.Duration(i)*2*time.Minute), 20+i, true, 5*time.Minute)
			}
			at, minutes, known := base.Add(10*time.Minute), 25, true
			switch kind {
			case "unknown":
				known = false
			case "gap":
				at = base.Add(20 * time.Minute)
			case "regression":
				minutes = 1
			case "clock":
				at = base
			}
			slow, _, _ := r.observe(at, minutes, known, 5*time.Minute)
			if slow || len(r.samples) > 1 {
				t.Fatal("invalid observation kept old window")
			}
		})
	}
	// Long-running storage stays bounded at the normal two-minute poll interval.
	var r watchRate
	for i := 0; i < 10000; i++ {
		r.observe(base.Add(time.Duration(i)*2*time.Minute), i*2, true, 5*time.Minute)
		if len(r.samples) > 6 {
			t.Fatal("unbounded samples")
		}
	}
}

func TestRateRecoveryCooldownSurvivesWindowReset(t *testing.T) {
	w := &accountWorker{}
	rnd := rand.New(rand.NewSource(1))
	now := time.Now()
	if !w.reserveRateRecovery(now, rnd) {
		t.Fatal("initial recovery blocked")
	}
	deadline := w.nextRateRecovery
	if deadline.Before(now.Add(10*time.Minute)) || deadline.After(now.Add(12*time.Minute)) {
		t.Fatal(deadline)
	}
	if w.reserveRateRecovery(deadline.Add(-time.Nanosecond), rnd) {
		t.Fatal("recovery repeated during cooldown")
	}
	if !w.reserveRateRecovery(deadline, rnd) {
		t.Fatal("cooldown did not expire")
	}
}

func TestSlowRecoveryChangesChannelOrReconnects(t *testing.T) {
	for _, alternative := range []bool{false, true} {
		t.Run(map[bool]string{true: "alternative", false: "only current channel"}[alternative], func(t *testing.T) {
			cfg := config.Default()
			cfg.TargetGame = "Game"
			cfg.LogDir = ""
			acc := &parser.Account{Login: "test"}
			m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
			var playback []string
			m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				if r.URL.Host == "gql.twitch.tv" {
					var p struct {
						Operation string         `json:"operationName"`
						Variables map[string]any `json:"variables"`
					}
					json.NewDecoder(r.Body).Decode(&p)
					switch p.Operation {
					case "GameSlug":
						body = `{"data":{"game":{"slug":"game"}}}`
					case "DirectoryPage_Game":
						nodes := `{"node":{"id":"2","broadcaster":{"id":"1","login":"current"}}}`
						if alternative {
							nodes += `,{"node":{"id":"3","broadcaster":{"id":"2","login":"other"}}}`
						}
						body = `{"data":{"game":{"streams":{"edges":[` + nodes + `]}}}}`
					case "PlaybackAccessToken":
						playback = append(playback, p.Variables["login"].(string))
						body = `{"data":{"streamPlaybackAccessToken":{"value":"test","signature":"test"}}}`
					default:
						t.Fatal(p.Operation)
					}
				} else if r.URL.Host == "usher.ttvnw.net" {
					body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://test.ttvnw.net/live.m3u8\n"
				} else {
					t.Fatal("unexpected host")
				}
				return reviewReply(body), nil
			})
			w := &accountWorker{m: m, st: m.states["test"], gql: twitch.NewGQLClient(m.http, cfg, acc)}
			selected, playlist, err := w.recoverSlowStream(context.Background(), rand.New(rand.NewSource(1)), twitch.Stream{Login: "current"})
			want := "current"
			if alternative {
				want = "other"
			}
			if err != nil || selected.Login != want || !strings.HasPrefix(playlist, "https://") || len(playback) != 1 || playback[0] != want {
				t.Fatalf("selected=%s calls=%v error=%v", selected.Login, playback, err)
			}
			if alternative && !time.Now().Before(w.unavailable["current"]) {
				t.Fatal("slow channel not excluded")
			}
			if !alternative && !w.unavailable["current"].IsZero() {
				t.Fatal("reconnected only channel remains excluded")
			}
		})
	}
}
