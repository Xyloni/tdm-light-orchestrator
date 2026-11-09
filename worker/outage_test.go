package worker

import (
	"context"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestTransientHLSKeepsCurrentStreamAndBeacon(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(old)
	m, workers, probes := signalFixture(t, 1, "")
	base := m.http.Transport
	var failed atomic.Bool
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "test.ttvnw.net" && r.Method == "GET" && !failed.Swap(true) {
			return nil, context.DeadlineExceeded
		}
		return base.RoundTrip(r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- workers[0].farmDropScheduled(ctx, rand.New(rand.NewSource(1)), twitch.Drop{ID: "drop", Required: 60}, watchIntervals{hls: 5 * time.Millisecond, beacon: 5 * time.Millisecond, progress: 5 * time.Millisecond})
	}()
	until := time.Now().Add(500 * time.Millisecond)
	for probes[0].calls[0].Load() < 3 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if probes[0].directories.Load() != 1 || probes[0].calls[0].Load() < 3 || probes[0].calls[1].Load() < 2 {
		t.Fatal("temporary HLS failure restarted stream or stopped beacon")
	}
}

func Test625RecoveryRequestsAreBoundedAndCancellationJoins(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(old)
	m, workers, _ := signalFixture(t, 625, "")
	var active, peak atomic.Int32
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(i int, w *accountWorker) { defer wg.Done(); w.openStream(ctx, rand.New(rand.NewSource(int64(i)))) }(i, w)
	}
	until := time.Now().Add(time.Second)
	for peak.Load() < 32 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued recovery did not cancel")
	}
	if peak.Load() != 32 || active.Load() != 0 || len(m.recoverySlots) != 0 {
		t.Fatal("recovery bound or cleanup failed", peak.Load(), active.Load())
	}
}
