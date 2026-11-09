package worker

import (
	"context"
	"sync"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

const (
	hlsCheckBudget       = 15 * time.Second
	streamRecoveryBudget = 20 * time.Second
	channelOpenBudget    = 8 * time.Second
)

type operationTimeouts struct{ hls, recovery, beacon, inventory time.Duration }

func (b operationTimeouts) defaults() operationTimeouts {
	if b.hls <= 0 {
		b.hls = hlsCheckBudget
	}
	if b.recovery <= 0 {
		b.recovery = streamRecoveryBudget
	}
	if b.beacon <= 0 {
		b.beacon = 15 * time.Second
	}
	if b.inventory <= 0 {
		b.inventory = 15 * time.Second
	}
	return b
}

// Only the HLS owner publishes streams. A generation context cancels the old
// beacon before it can continue on a replaced stream; cadence is not reset.
type watchStream struct {
	mu      sync.Mutex
	parent  context.Context
	stream  twitch.Stream
	ctx     context.Context
	cancel  context.CancelFunc
	changed chan struct{}
}

func newWatchStream(parent context.Context) *watchStream {
	return &watchStream{parent: parent, changed: make(chan struct{}, 1)}
}

func (s *watchStream) set(stream twitch.Stream) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.stream = stream
	s.ctx, s.cancel = context.WithCancel(s.parent)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *watchStream) clear() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx = nil
	s.cancel = nil
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *watchStream) current() (twitch.Stream, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream, s.ctx
}

func (w *accountWorker) beaconLoop(ctx context.Context, streams *watchStream, interval, budget time.Duration) {
	next := time.Time{}
	for ctx.Err() == nil {
		stream, streamCtx := streams.current()
		if streamCtx == nil {
			select {
			case <-ctx.Done():
				return
			case <-streams.changed:
			}
			continue
		}
		if time.Now().Before(next) {
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-streams.changed:
				timer.Stop()
				continue
			case <-timer.C:
			}
		}
		started := time.Now()
		requestCtx, stop := context.WithTimeout(streamCtx, budget)
		err := w.gql.SendMinuteWatched(requestCtx, stream)
		stop()
		if ctx.Err() != nil {
			return
		}
		next = signalDeadline(next, started, time.Now(), interval)
		if streamCtx.Err() != nil {
			continue
		}
		if err != nil {
			w.st.addLog("minute-watched beacon: " + err.Error())
		} else {
			w.st.addLog("minute-watched beacon отправлен")
		}
	}
}

type inventoryResult struct {
	drop             twitch.Drop
	found            bool
	err              error
	started, checked time.Time
}

func (w *accountWorker) inventoryLoop(ctx context.Context, dropID string, interval, budget time.Duration, results chan inventoryResult) {
	next := time.Now().Add(interval)
	for sleepUntil(ctx, next) && ctx.Err() == nil {
		started := time.Now()
		requestCtx, stop := context.WithTimeout(ctx, budget)
		drop, found, err := w.findDrop(requestCtx, dropID)
		stop()
		if ctx.Err() != nil {
			return
		}
		checked := time.Now()
		next = signalDeadline(next, started, checked, interval)
		result := inventoryResult{drop: drop, found: found, err: err, started: started, checked: checked}
		// Keep only the latest result while HLS/recovery is busy. Terminal results
		// remain queued: do not overwrite completion/disappearance with a later poll.
		select {
		case results <- result:
		default:
			select {
			case <-results:
			default:
			}
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}
		}
		if err == nil && (!found || drop.IsClaimed || (drop.ProgressKnown && drop.Current >= drop.Required)) {
			return
		}
	}
}
