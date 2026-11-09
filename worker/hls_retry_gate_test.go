package worker

import (
	"context"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestHLSRetryGateRelaxesAfterBurst(t *testing.T) {
	var gate hlsRetryGate
	now := time.Now()
	for i := 0; i < hlsBurstThreshold-1; i++ {
		gate.failed(now)
	}
	fill := func(at time.Time, limit int) {
		t.Helper()
		for i := 0; i < limit; i++ {
			if !gate.tryStart(at) {
				t.Fatal("unexpected throttle", i, limit)
			}
		}
	}
	drain := func(n int) {
		for i := 0; i < n; i++ {
			gate.finish()
		}
	}
	fill(now, 1000)
	drain(1000)
	gate.failed(now)
	for _, phase := range []struct {
		elapsed time.Duration
		limit   int
	}{{0, hlsRetryLimit}, {hlsRecoveryPhase, 2 * hlsRetryLimit}} {
		fill(now.Add(phase.elapsed), phase.limit)
		if gate.tryStart(now.Add(phase.elapsed)) {
			t.Fatal("retry passed limit")
		}
		drain(phase.limit)
	}
	fill(now.Add(2*hlsRecoveryPhase), 1000)
	drain(1000)
}

func Test1000WorkersRecoverFromSharedHLSOutage(t *testing.T) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(old)
	const n = 1000
	m, workers, probes := signalFixture(t, n, "")
	base := m.http.Transport
	var phase atomic.Int32
	var failed [n]atomic.Bool
	var active, peak atomic.Int32
	resume := make(chan struct{})
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "usher.ttvnw.net" && phase.Load() == 1 {
			index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/channel/hls/live-"), ".m3u8"))
			if err == nil && index >= 0 && index < n && failed[index].Load() {
				// Fail the inline playlist refresh as part of the first attempt.
				return nil, context.DeadlineExceeded
			}
		}
		if r.URL.Host == "test.ttvnw.net" && r.Method == "GET" && phase.Load() == 1 {
			index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/live-"), ".m3u8"))
			if err != nil || index < 0 || index >= n {
				t.Error("invalid fixture path")
				return nil, context.Canceled
			}
			if !failed[index].Swap(true) {
				return nil, context.DeadlineExceeded
			}
			count := active.Add(1)
			defer active.Add(-1)
			for p := peak.Load(); count > p && !peak.CompareAndSwap(p, count); p = peak.Load() {
			}
			select {
			case <-resume:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return base.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for i, w := range workers {
		wg.Add(1)
		go func(i int, w *accountWorker) {
			defer wg.Done()
			err := w.farmDropScheduled(ctx, rand.New(rand.NewSource(int64(i))), twitch.Drop{ID: "drop", Required: 60}, watchIntervals{hls: 20 * time.Millisecond, beacon: 40 * time.Millisecond, progress: 40 * time.Millisecond, timeouts: operationTimeouts{hls: 15 * time.Second, recovery: 15 * time.Second}})
			if err != nil {
				t.Error(err)
			}
		}(i, w)
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for test phase")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(func() bool {
		for i := range probes {
			if probes[i].calls[0].Load() < 2 {
				return false
			}
		}
		return true
	})
	// Pre-arm the shared state to isolate admission from scheduling jitter.
	// The threshold and exit policy are exercised separately.
	for i := 0; i < hlsBurstThreshold; i++ {
		m.hlsRetries.failed(time.Now())
	}
	phase.Store(1)
	wait(func() bool {
		for i := range failed {
			if !failed[i].Load() {
				return false
			}
		}
		return peak.Load() >= hlsRetryLimit
	})
	var background [n][2]int32
	for i := range probes {
		background[i] = [2]int32{probes[i].calls[1].Load(), probes[i].calls[2].Load()}
	}
	wait(func() bool {
		for i := range probes {
			if probes[i].calls[1].Load() <= background[i][0] || probes[i].calls[2].Load() <= background[i][1] {
				return false
			}
		}
		return true
	})
	phase.Store(2)
	var before [n]int32
	for i := range probes {
		before[i] = probes[i].calls[0].Load()
	}
	close(resume)
	wait(func() bool {
		for i := range probes {
			if probes[i].calls[0].Load() < before[i]+2 {
				return false
			}
		}
		return true
	})
	if peak.Load() != hlsRetryLimit || active.Load() != 0 {
		t.Fatal("retry limit or cleanup failed", peak.Load(), active.Load())
	}
}
