package worker

import (
	"fmt"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func (s *AccountState) setProgress(drop twitch.Drop) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CurrentDrop = drop.Name
	s.CurrentMinutes = drop.Current
	s.RequiredMinutes = drop.Required
	s.ProgressKnown = drop.ProgressKnown
	s.ProgressChecked = time.Now()
	s.observePendingLocked(drop)
	s.recordRewardProgressLocked(drop, false)
}

func (s *AccountState) recordRewardProgressLocked(drop twitch.Drop, ready bool) {
	earned := drop.IsClaimed || (drop.ProgressKnown && drop.Required > 0 && drop.Current >= drop.Required)
	if !s.trackStats || drop.ID == "" || (!drop.ProgressKnown && !ready && !earned) {
		return
	}
	if s.rewardProgress == nil {
		s.rewardProgress = make(map[string]ManagedRewardProgress)
	}
	item := s.rewardProgress[drop.ID]
	item.ID = drop.ID
	item.Name = drop.Name
	if drop.ProgressKnown {
		current := max(0, min(drop.Current, drop.Required))
		if !item.Known {
			item.First = current
		}
		item.Known = true
		item.Minutes = max(item.Minutes, current)
	}
	item.Ready = item.Ready || ready
	item.Earned = item.Earned || earned
	item.Claimed = item.Claimed || drop.IsClaimed
	if item.Claimed {
		item.ClaimError = ""
	}
	s.rewardProgress[drop.ID] = item
}

func (s *AccountState) observePendingLocked(drop twitch.Drop) {
	if drop.ID != "" && drop.ProgressKnown && !drop.IsClaimed && drop.Required > 0 && drop.Current < drop.Required {
		if s.pendingDrops == nil {
			s.pendingDrops = make(map[string]struct{})
		}
		s.pendingDrops[drop.ID] = struct{}{}
	}
}

func (w *accountWorker) reportProgress(drop twitch.Drop) {
	w.st.setProgress(drop)
	if !drop.ProgressKnown {
		w.st.addLog("прогресс «" + drop.Name + "»: личные минуты пока не получены из Twitch Inventory")
		return
	}
	w.st.addLog(fmt.Sprintf("прогресс «%s»: %d/%d мин (Twitch Inventory)", drop.Name, drop.Current, drop.Required))
}
