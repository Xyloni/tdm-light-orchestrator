package worker

import (
	"testing"
	"time"
)

func TestStartupDelayDistribution(t *testing.T) {
	cases := []struct {
		count int
		want  []time.Duration
	}{
		{count: 5, want: []time.Duration{0, 12 * time.Second, 24 * time.Second, 36 * time.Second, 48 * time.Second}},
		{count: 1, want: []time.Duration{0}},
	}
	for _, tc := range cases {
		for i, want := range tc.want {
			if got := startupDelay(i, tc.count); got != want {
				t.Errorf("startupDelay(%d, %d) = %s, want %s", i, tc.count, got, want)
			}
		}
	}

	const count = 200
	step := time.Minute / count
	if step != 300*time.Millisecond {
		t.Fatalf("200-account interval = %s, want 300ms", step)
	}
	if got := startupDelay(count-1, count); got != 59_700*time.Millisecond {
		t.Fatalf("last account starts at %s, want 59.7s", got)
	}
}

func TestHeartbeatDeadlineCadence(t *testing.T) {
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	interval := time.Minute
	first := heartbeatDeadline(time.Time{}, base, base.Add(10*time.Second), interval)
	if want := base.Add(interval); !first.Equal(want) {
		t.Fatalf("first deadline = %s, want %s", first, want)
	}

	second := heartbeatDeadline(first, first, first.Add(500*time.Millisecond), interval)
	if want := first.Add(interval); !second.Equal(want) {
		t.Fatalf("second deadline = %s, want %s", second, want)
	}

	// If a heartbeat takes longer than its slot, wait a full interval after
	// completion instead of immediately sending a catch-up request.
	now := second.Add(interval + time.Second)
	slow := heartbeatDeadline(second, second, now, interval)
	if want := now.Add(interval); !slow.Equal(want) {
		t.Fatalf("slow-request deadline = %s, want %s", slow, want)
	}
}
