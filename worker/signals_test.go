package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type signalProbe struct {
	calls, active, peak [3]atomic.Int32 // HLS, beacon, Inventory
	directories         atomic.Int32
}

func (p *signalProbe) enter(kind int) func() {
	p.calls[kind].Add(1)
	active := p.active[kind].Add(1)
	for old := p.peak[kind].Load(); active > old; old = p.peak[kind].Load() {
		if p.peak[kind].CompareAndSwap(old, active) {
			break
		}
	}
	return func() { p.active[kind].Add(-1) }
}

func signalFixture(t *testing.T, n int, mode string) (*Manager, []*accountWorker, []signalProbe) {
	t.Helper()
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	accounts := make([]*parser.Account, n)
	for i := range accounts {
		accounts[i] = &parser.Account{Login: fmt.Sprintf("fixture%d", i), UserID: fmt.Sprint(i + 1), DeviceID: fmt.Sprint(i)}
	}
	m := NewManager(cfg, [][]*parser.Account{accounts}, make(chan struct{}, 1))
	probes := make([]signalProbe, n)
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		indexText := r.Header.Get("X-Device-Id")
		var payload struct {
			Operation string         `json:"operationName"`
			Variables map[string]any `json:"variables"`
		}
		if r.URL.Host == "gql.twitch.tv" {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return nil, err
			}
		}
		if indexText == "" {
			component := strings.TrimPrefix(r.URL.Path, "/api/channel/hls/")
			component = strings.TrimPrefix(component, "/")
			component = strings.TrimSuffix(strings.TrimSuffix(component, ".m3u8"), ".ts")
			component = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(component, "bad-"), "live-"), "segment-")
			indexText = component
		}
		index, err := strconv.Atoi(indexText)
		if err != nil || index < 0 || index >= n {
			t.Errorf("unexpected fixture request %s", r.URL.Path)
			return nil, fmt.Errorf("bad fixture index")
		}
		probe := &probes[index]
		kind := -1
		if payload.Operation == "Inventory" {
			kind = 2
		}
		if r.URL.Host == "spade.twitch.tv" {
			kind = 1
		}
		if r.URL.Host == "test.ttvnw.net" && r.Method == "GET" {
			kind = 0
		}
		if kind >= 0 {
			defer probe.enter(kind)()
		}
		blocked := (mode == "inventory" && kind == 2) || (mode == "beacon" && kind == 1) || (mode == "hls" && kind == 0) ||
			(mode == "complete-hls" && kind == 0) || (mode == "complete-beacon" && kind == 1) ||
			(mode == "switch" && kind == 0 && strings.Contains(r.URL.Path, "bad-")) ||
			(mode == "open-stall" && payload.Operation == "DirectoryPage_Game")
		if blocked {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		body := ""
		status := 200
		switch r.URL.Host {
		case "gql.twitch.tv":
			switch payload.Operation {
			case "GameSlug":
				body = `{"data":{"game":{"slug":"game"}}}`
			case "DirectoryPage_Game":
				count := probe.directories.Add(1)
				login := fmt.Sprintf("live-%d", index)
				if mode == "switch" && count == 1 {
					login = fmt.Sprintf("bad-%d", index)
				}
				body = fmt.Sprintf(`{"data":{"game":{"streams":{"edges":[{"node":{"id":"2","game":{"id":"3"},"broadcaster":{"id":"1","login":%q}}}]}}}}`, login)
			case "PlaybackAccessToken":
				body = `{"data":{"streamPlaybackAccessToken":{"value":"fixture","signature":"fixture"}}}`
			case "Inventory":
				minutes := 1
				if (mode == "open-stall" || strings.HasPrefix(mode, "complete-")) && probe.calls[2].Load() >= 3 {
					minutes = 60
				}
				body = fmt.Sprintf(`{"data":{"currentUser":{"inventory":{"dropCampaignsInProgress":[{"id":"campaign","game":{"name":"Game"},"timeBasedDrops":[{"id":"drop","name":"Reward","requiredMinutesWatched":60,"self":{"currentMinutesWatched":%d}}]}]}}}}`, minutes)
			default:
				t.Errorf("unexpected operation %s", payload.Operation)
			}
		case "usher.ttvnw.net":
			login := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/channel/hls/"), ".m3u8")
			body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://test.ttvnw.net/" + login + ".m3u8\n"
		case "test.ttvnw.net":
			body = fmt.Sprintf("#EXTM3U\n#EXTINF:2,\nsegment-%d.ts\n", index)
		case "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case "spade.twitch.tv":
			status = 204
		default:
			t.Errorf("unexpected host %s", r.URL.Host)
		}
		reply := reviewReply(body)
		reply.StatusCode = status
		return reply, nil
	})
	workers := make([]*accountWorker, n)
	for i, acc := range accounts {
		workers[i] = &accountWorker{m: m, acc: acc, st: m.states[acc.Login], gql: twitch.NewGQLClient(m.http, cfg, acc)}
	}
	return m, workers, probes
}

func Test250And500WorkersIsolateStalledSignals(t *testing.T) {
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)
	for _, scenario := range []struct {
		n       int
		mode    string
		blocked int
	}{{250, "inventory", 2}, {500, "inventory", 2}, {500, "beacon", 1}, {500, "hls", 0}, {625, "hls", 0}, {625, "beacon", 1}, {625, "inventory", 2}} {
		t.Run(fmt.Sprintf("%d/%s", scenario.n, scenario.mode), func(t *testing.T) {
			_, workers, probes := signalFixture(t, scenario.n, scenario.mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var wg sync.WaitGroup
			wg.Add(len(workers))
			const stalledTimeout = 25 * time.Second
			intervals := watchIntervals{
				hls: 100 * time.Millisecond, beacon: 200 * time.Millisecond, progress: 200 * time.Millisecond,
				timeouts: operationTimeouts{hls: stalledTimeout, recovery: stalledTimeout, beacon: stalledTimeout, inventory: stalledTimeout},
			}
			for i, w := range workers {
				go func(i int, w *accountWorker) {
					defer wg.Done()
					err := w.farmDropScheduled(ctx, rand.New(rand.NewSource(int64(i))), twitch.Drop{ID: "drop", Name: "Reward", Required: 60}, intervals)
					if err != nil {
						t.Error(err)
					}
				}(i, w)
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			allObserved := false
			// Allow for race-detector overhead on CI while keeping the
			// observation window shorter than the blocked operation's timeout.
			deadline := time.Now().Add(12 * time.Second)
			for time.Now().Before(deadline) {
				allObserved = true
				for i := range probes {
					for kind := 0; kind < 3; kind++ {
						want := int32(2)
						if kind == scenario.blocked {
							want = 1
						}
						if probes[i].calls[kind].Load() < want {
							allObserved = false
						}
					}
				}
				if allObserved {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("worker shutdown did not join all signal loops")
			}
			if !allObserved {
				t.Fatal("a stalled signal prevented other signals from running on all accounts")
			}
			for i := range probes {
				for kind := 0; kind < 3; kind++ {
					if probes[i].active[kind].Load() != 0 || probes[i].peak[kind].Load() > 1 {
						t.Fatalf("account %d kind %d active=%d peak=%d", i, kind, probes[i].active[kind].Load(), probes[i].peak[kind].Load())
					}
				}
			}
		})
	}
}

func TestHLSBudgetSwitchesAwayFromHangingChannel(t *testing.T) {
	_, workers, probes := signalFixture(t, 1, "switch")
	w := workers[0]
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- w.farmDropScheduled(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Required: 60}, watchIntervals{hls: 10 * time.Millisecond, beacon: 10 * time.Millisecond, progress: 10 * time.Millisecond, timeouts: operationTimeouts{hls: 35 * time.Millisecond, recovery: 100 * time.Millisecond}})
	}()
	for probes[0].directories.Load() < 2 && time.Since(started) < 500*time.Millisecond {
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(started)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker stuck")
	}
	if probes[0].calls[0].Load() < 2 || probes[0].directories.Load() < 2 || elapsed > 500*time.Millisecond {
		t.Fatal("HLS budget did not switch the channel")
	}
	if !time.Now().Before(w.unavailable["bad-0"]) {
		t.Fatal("failed channel not cooled down")
	}
	if probes[0].calls[1].Load() < 2 || probes[0].calls[2].Load() < 2 {
		t.Fatal("HLS stall blocked beacon or Inventory")
	}
}

func TestInventoryCompletesDropWhileOpeningChannelStalls(t *testing.T) {
	_, workers, probes := signalFixture(t, 1, "open-stall")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err := workers[0].farmDropScheduled(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Required: 60}, watchIntervals{hls: 10 * time.Millisecond, beacon: 10 * time.Millisecond, progress: 5 * time.Millisecond, timeouts: operationTimeouts{recovery: 35 * time.Millisecond}})
	if err != nil || ctx.Err() != nil || time.Since(start) > 500*time.Millisecond || workers[0].st.DropsReady != 1 || probes[0].calls[2].Load() < 3 {
		t.Fatal("opening stream blocked completion", err)
	}
}

func TestReadyDropJoinsAllOldSignalRequests(t *testing.T) {
	for _, mode := range []string{"complete-hls", "complete-beacon"} {
		t.Run(mode, func(t *testing.T) {
			_, workers, probes := signalFixture(t, 1, mode)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := workers[0].farmDropScheduled(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Required: 60}, watchIntervals{hls: 5 * time.Millisecond, beacon: 5 * time.Millisecond, progress: 5 * time.Millisecond, timeouts: operationTimeouts{hls: 35 * time.Millisecond}})
			if err != nil || ctx.Err() != nil || workers[0].st.DropsReady != 1 {
				t.Fatal("drop completion failed", err)
			}
			for kind := 0; kind < 3; kind++ {
				if probes[0].active[kind].Load() != 0 {
					t.Fatal("request from completed drop still active", kind)
				}
			}
		})
	}
}

func TestBeaconStreamChangeCancelsOldRequestWithoutBurst(t *testing.T) {
	_, workers, _ := signalFixture(t, 1, "beacon")
	w := workers[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streams := newWatchStream(ctx)
	defer streams.clear()
	oldStarted := make(chan time.Time, 1)
	oldCancelled := make(chan struct{})
	newSent := make(chan time.Time, 1)
	w.gql = twitch.NewGQLClient(&http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "www.twitch.tv" {
			return reviewReply(`{"spade_url":"https://spade.twitch.tv/track"}`), nil
		}
		if r.URL.Host == "spade.twitch.tv" {
			if r.Header.Get("X-Device-Id") == "" {
				t.Error("missing account headers")
			}
			// The beacon payload identifies the selected stream.
			body, _ := io.ReadAll(r.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Error(err)
				return nil, err
			}
			decoded, err := base64.StdEncoding.DecodeString(form.Get("data"))
			if err != nil {
				t.Error(err)
				return nil, err
			}
			var events []struct {
				Properties struct {
					Channel string `json:"channel"`
				} `json:"properties"`
			}
			if err = json.Unmarshal(decoded, &events); err != nil || len(events) != 1 {
				t.Error("invalid beacon payload")
				return nil, fmt.Errorf("bad beacon")
			}
			switch events[0].Properties.Channel {
			case "new":
				select {
				case newSent <- time.Now():
				default:
				}
				reply := reviewReply("")
				reply.StatusCode = 204
				return reply, nil
			case "old":
				oldStarted <- time.Now()
				<-r.Context().Done()
				close(oldCancelled)
				return nil, r.Context().Err()
			default:
				t.Error("unexpected beacon channel")
				return nil, fmt.Errorf("bad channel")
			}
		}
		return nil, fmt.Errorf("unexpected request")
	})}, w.m.cfg, &parser.Account{UserID: "1", DeviceID: "fixture"})
	stream := twitch.Stream{Login: "old", ChannelID: "1", BroadcastID: "2", GameID: "3"}
	streams.set(stream)
	done := make(chan struct{})
	go func() { w.beaconLoop(ctx, streams, 100*time.Millisecond, time.Second); close(done) }()
	oldAt := <-oldStarted
	stream.Login = "new"
	streams.set(stream)
	select {
	case <-oldCancelled:
	case <-time.After(time.Second):
		t.Fatal("old beacon not cancelled")
	}
	select {
	case when := <-newSent:
		if when.Sub(oldAt) < 60*time.Millisecond {
			t.Fatal("stream switch caused an immediate duplicate beacon")
		}
	case <-time.After(time.Second):
		t.Fatal("new stream never received beacon")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("beacon loop leaked")
	}
}

func TestLogDateAndTimeUseSameInstantAtMidnight(t *testing.T) {
	s := &AccountState{Account: &parser.Account{Login: "fixture"}, LogFile: filepath.Join(t.TempDir(), "fixture.log")}
	zone := time.FixedZone("test", 3*60*60)
	s.addLogAt(time.Date(2026, 11, 2, 23, 59, 59, 0, zone), "before")
	s.addLogAt(time.Date(2026, 11, 3, 0, 0, 0, 0, zone), "after")
	data, err := os.ReadFile(s.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "2026-11-02 23:59:59 before\n2026-11-03 00:00:00 after\n" {
		t.Fatalf("mixed midnight timestamps: %q", data)
	}
}
