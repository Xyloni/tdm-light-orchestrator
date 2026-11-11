package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

const maxClaimAttempts = 3

var errClaimRefresh = errors.New("inventory refresh after claim")

func (w *accountWorker) claimReadyDrop(ctx context.Context, drop twitch.Drop) error {
	key := drop.CampaignID + "\x00" + drop.ID
	if w.claimAttempts[key] >= maxClaimAttempts {
		return nil
	}
	if w.claimAttempts == nil {
		w.claimAttempts = make(map[string]int)
	}
	w.claimAttempts[key]++
	verified, err := w.gql.ClaimAndVerifyDrop(ctx, drop)
	if err == nil {
		w.markReady(verified)
		w.st.addLog("🎁 Получено на Twitch: " + drop.Name)
		return errClaimRefresh
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if w.claimAttempts[key] == maxClaimAttempts {
		w.st.mu.Lock()
		if item, ok := w.st.rewardProgress[drop.ID]; ok {
			item.ClaimError = err.Error()
			w.st.rewardProgress[drop.ID] = item
		}
		w.st.mu.Unlock()
		w.st.addLog(fmt.Sprintf("Автополучение «%s» не подтверждено после %d попыток: %v", drop.Name, maxClaimAttempts, err))
		return nil
	}
	return fmt.Errorf("автополучение «%s»: %w", drop.Name, err)
}
