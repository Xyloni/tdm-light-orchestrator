package worker

import (
	"sync"
	"time"
)

const (
	hlsBurstWindow    = 10 * time.Second
	hlsBurstThreshold = 16
	hlsRetryLimit     = 32
	hlsRecoveryPhase  = 30 * time.Second
)

// The gate tracks retries across the manager. First attempts bypass it.
type hlsRetryGate struct {
	mu          sync.Mutex
	failures    [hlsBurstThreshold]time.Time
	nextFailure int
	lastBurst   time.Time
	active      int
}

func (g *hlsRetryGate) failed(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures[g.nextFailure] = now
	g.nextFailure = (g.nextFailure + 1) % len(g.failures)
	oldest := g.failures[g.nextFailure]
	if !oldest.IsZero() && now.Sub(oldest) <= hlsBurstWindow {
		g.lastBurst = now
	}
}

func (g *hlsRetryGate) tryStart(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.lastBurst.IsZero() {
		quiet := now.Sub(g.lastBurst)
		switch {
		case quiet < hlsRecoveryPhase && g.active >= hlsRetryLimit:
			return false
		case quiet < 2*hlsRecoveryPhase && g.active >= 2*hlsRetryLimit:
			return false
		}
	}
	g.active++
	return true
}

func (g *hlsRetryGate) finish() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}
