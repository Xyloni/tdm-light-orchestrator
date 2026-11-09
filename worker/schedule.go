package worker

import "time"

// Advance a fixed schedule past missed slots without a burst of catch-up calls.
func signalDeadline(previous, started, now time.Time, interval time.Duration) time.Time {
	if previous.IsZero() {
		previous = started
	}
	next := previous.Add(interval)
	if !next.After(now) {
		next = next.Add((now.Sub(next)/interval + 1) * interval)
	}
	return next
}

// startupDelay spreads account starts evenly across the first minute.
func startupDelay(index, count int) time.Duration {
	if count <= 1 || index <= 0 {
		return 0
	}
	if index >= count {
		index = count - 1
	}
	return time.Duration(index) * time.Minute / time.Duration(count)
}

// heartbeatDeadline keeps a fixed cadence based on request start times,
// avoiding drift from request processing time.
func heartbeatDeadline(previous, started, now time.Time, interval time.Duration) time.Time {
	if previous.IsZero() {
		return started.Add(interval)
	}
	next := previous.Add(interval)
	if !next.After(now) {
		return now.Add(interval)
	}
	return next
}
