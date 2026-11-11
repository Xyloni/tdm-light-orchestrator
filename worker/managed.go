package worker

import (
	"context"
	"sync"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

// SetCampaignStreams publishes an immutable list to all managed workers.
func (m *Manager) SetCampaignStreams(streams []twitch.Stream) {
	if streams == nil {
		m.campaignStreams.Store(nil)
		return
	}
	m.campaignStreams.Store(&streams)
}

func (m *Manager) allowsStream(stream twitch.Stream) bool {
	allowed := m.campaignStreams.Load()
	if allowed == nil {
		return true
	}
	for _, candidate := range *allowed {
		if candidate.ChannelID == stream.ChannelID && candidate.BroadcastID == stream.BroadcastID && candidate.GameID == stream.GameID {
			return true
		}
	}
	return false
}

type ManagedRewardProgress struct {
	ClaimError      string
	Name            string
	ID              string
	First, Minutes  int
	Known, Ready    bool
	Earned, Claimed bool
}

type ManagedProgress struct {
	UserID                string
	RewardProgress        []ManagedRewardProgress
	Login                 string
	Status                string
	Required, Current     int
	ProgressKnown         bool
	RemainingAfterCurrent int
	Rate                  float64
	RateKnown             bool
	RateChecked           time.Time
	LongRate              float64
	LongRateKnown         bool
	LongRateChecked       time.Time
	Recovery              ManagedRecovery
}

// RunManagedGroup runs exactly one group. Only explicitly selected accounts
// are cancelled by remove; their siblings continue with the same contexts.
func (m *Manager) RunManagedGroup(ctx context.Context, group []*parser.Account, remove <-chan []string) {
	defer m.http.CloseIdleConnections()
	var workers sync.WaitGroup
	stops := make(map[string]context.CancelFunc, len(group))
	for index, account := range group {
		child, stop := context.WithCancel(ctx)
		stops[account.Login] = stop
		w := m.managedWorker(group, index, account)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer stop()
			w.Run(child)
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case names, ok := <-remove:
			if !ok {
				remove = nil
				continue
			}
			for _, name := range names {
				if stop := stops[name]; stop != nil {
					stop()
				}
			}
		}
	}
}

func (m *Manager) ManagedRewardSummaries() []string {
	return m.readySummaries(false)
}

func (m *Manager) ManagedSnapshot() []ManagedProgress {
	out := make([]ManagedProgress, 0, len(m.states))
	for login, s := range m.states {
		s.mu.Lock()
		p := ManagedProgress{Login: login, UserID: s.Account.UserID, Status: s.Status, Required: s.RequiredMinutes, RemainingAfterCurrent: s.remainingAfterCurrent}
		p.Recovery = s.recoveryStatus.ManagedRecovery
		if s.ProgressKnown {
			p.ProgressKnown = true
			p.Current = s.CurrentMinutes
		}
		if s.rateSnapshot.ready {
			p.RateKnown = true
			p.RateChecked = s.rateSnapshot.checked
			p.Rate = float64(s.rateSnapshot.gain) / s.rateSnapshot.elapsed.Minutes()
		}
		if s.longRateSnapshot.ready {
			p.LongRateKnown = true
			p.LongRateChecked = s.longRateSnapshot.checked
			p.LongRate = float64(s.longRateSnapshot.gain) / s.longRateSnapshot.elapsed.Minutes()
		}
		for _, reward := range s.rewardProgress {
			p.RewardProgress = append(p.RewardProgress, reward)
		}
		s.mu.Unlock()
		out = append(out, p)
	}
	return out
}
