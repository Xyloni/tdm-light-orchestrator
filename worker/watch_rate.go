package worker

import (
	"math/rand"
	"time"
)

const (
	rateWindow             = 10 * time.Minute
	rateMaxWindow          = 15 * time.Minute
	rateRecoveryCooldown   = 10 * time.Minute
	sustainedRateWindow    = 30 * time.Minute
	sustainedRateMaxWindow = 35 * time.Minute
)

type progressSample struct {
	at      time.Time
	minutes int
}
type watchRate struct{ samples []progressSample }

type rateSnapshot struct {
	gain    int
	elapsed time.Duration
	started time.Time
	checked time.Time
	ready   bool
}

func (r *watchRate) snapshot() rateSnapshot {
	snapshot := rateSnapshot{}
	if len(r.samples) >= 4 {
		first, last := r.samples[0], r.samples[len(r.samples)-1]
		elapsed := last.at.Sub(first.at)
		if elapsed >= rateWindow && elapsed <= rateMaxWindow {
			snapshot = rateSnapshot{gain: last.minutes - first.minutes, elapsed: elapsed, started: first.at, checked: last.at, ready: true}
		}
	}
	return snapshot
}

func (s *AccountState) setWatchRate(r *watchRate) {
	snapshot := r.snapshot()
	s.mu.Lock()
	s.rateSnapshot = snapshot
	s.mu.Unlock()
}

func (s *AccountState) clearWatchRate() {
	s.mu.Lock()
	s.rateSnapshot = rateSnapshot{}
	s.mu.Unlock()
}

func (r *watchRate) reset(at time.Time, minutes int, known bool) {
	r.samples = r.samples[:0]
	if known {
		r.samples = append(r.samples, progressSample{at, minutes})
	}
}

// A small positive increment does not reset the observation window. Unknown
// progress, a backwards clock/counter, or a long observation gap does.
func (r *watchRate) observe(at time.Time, minutes int, known bool, maxGap time.Duration) (bool, int, time.Duration) {
	return r.observeWindow(at, minutes, known, maxGap, rateWindow, rateMaxWindow, 0.8)
}

func (r *watchRate) observeWindow(at time.Time, minutes int, known bool, maxGap, window, maxWindow time.Duration, threshold float64) (bool, int, time.Duration) {
	if !known {
		r.reset(at, minutes, false)
		return false, 0, 0
	}
	if len(r.samples) > 0 {
		last := r.samples[len(r.samples)-1]
		if !at.After(last.at) || minutes < last.minutes || at.Sub(last.at) > maxGap {
			r.reset(at, minutes, true)
			return false, 0, 0
		}
	}
	r.samples = append(r.samples, progressSample{at, minutes})
	for len(r.samples) > 1 && at.Sub(r.samples[1].at) >= window {
		r.samples = r.samples[1:]
	}
	first := r.samples[0]
	elapsed, gain := at.Sub(first.at), minutes-first.minutes
	if elapsed > maxWindow {
		r.reset(at, minutes, true)
		return false, 0, 0
	}
	// One minute allowance for integer rounding and reporting lag.
	slow := len(r.samples) >= 4 && elapsed >= window && gain+1 < int(elapsed.Minutes()*threshold)
	return slow, gain, elapsed
}

func (w *accountWorker) reserveRateRecovery(now time.Time, rnd *rand.Rand) bool {
	if now.Before(w.nextRateRecovery) {
		return false
	}
	w.nextRateRecovery = now.Add(rateRecoveryCooldown + time.Duration(rnd.Intn(121))*time.Second)
	return true
}

// The long window catches persistent moderate loss. Require the recent short
// window to still be below 90%, so an old outage does not restart recovered viewing.
func (r *watchRate) observeSustained(at time.Time, minutes int, known bool, maxGap time.Duration, recentGain int, recentElapsed time.Duration) (bool, int, time.Duration) {
	slow, gain, elapsed := r.observeWindow(at, minutes, known, maxGap, sustainedRateWindow, sustainedRateMaxWindow, 0.9)
	return slow && recentElapsed >= rateWindow && float64(recentGain)+0.5 < recentElapsed.Minutes()*0.9, gain, elapsed
}
