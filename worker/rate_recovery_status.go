package worker

import "time"

type ManagedRecovery struct {
	Attempts int
	State    string
	Rate     float64
	At       time.Time
}

type rateRecoveryStatus struct {
	ManagedRecovery
	dropID string
	ended  time.Time
}

func (s *AccountState) beginRateRecovery(dropID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateRecoveries++
	s.recoveryStatus = rateRecoveryStatus{
		ManagedRecovery: ManagedRecovery{Attempts: s.rateRecoveries, State: "running", At: now},
		dropID:          dropID,
	}
}

func (s *AccountState) endRateRecovery(now time.Time, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveryStatus.State = state
	s.recoveryStatus.ended = now
}

func (s *AccountState) observeRateRecovery(dropID string, finished bool, r *watchRate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recovery := &s.recoveryStatus
	if recovery.State != "measuring" {
		return
	}
	if dropID != recovery.dropID {
		recovery.State = "unmeasured"
		return
	}
	snapshot := s.rateSnapshot
	if r != nil {
		snapshot = r.snapshot()
	}
	if snapshot.ready && !snapshot.started.Before(recovery.ended) {
		recovery.Rate = float64(snapshot.gain) / snapshot.elapsed.Minutes()
		recovery.State = "still_slow"
		if recovery.Rate >= NormalWatchRate {
			recovery.State = "normal"
		}
	} else if finished {
		recovery.State = "unmeasured"
	}
}
