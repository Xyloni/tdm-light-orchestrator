package worker

import (
	"testing"
	"time"
)

func TestRecoveryResultRequiresPostAttemptWindow(t *testing.T) {
	now := time.Now()
	var s AccountState
	s.beginRateRecovery("drop", now)
	s.endRateRecovery(now, "measuring")
	s.rateSnapshot = rateSnapshot{ready: true, started: now.Add(-time.Minute), gain: 10, elapsed: 10 * time.Minute}
	s.observeRateRecovery("drop", false, nil)
	if s.recoveryStatus.State != "measuring" {
		t.Fatal("mixed window was used")
	}
	r := watchRate{samples: []progressSample{{at: now, minutes: 0}, {at: now.Add(2 * time.Minute), minutes: 2}, {at: now.Add(6 * time.Minute), minutes: 6}, {at: now.Add(10 * time.Minute), minutes: 9}}}
	s.observeRateRecovery("drop", false, &r)
	if s.recoveryStatus.State != "normal" || s.recoveryStatus.Attempts != 1 {
		t.Fatalf("unexpected recovery: %+v", s.recoveryStatus)
	}
	s.beginRateRecovery("drop", now)
	s.endRateRecovery(now, "failed")
	s.observeRateRecovery("drop", false, &r)
	if s.recoveryStatus.State != "failed" || s.recoveryStatus.Attempts != 2 {
		t.Fatal("failed attempt was overwritten")
	}
}
