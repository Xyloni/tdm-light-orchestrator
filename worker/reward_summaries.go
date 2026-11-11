package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type rewardBatchKey struct {
	group          int
	campaign, drop string
}
type rewardBatch struct {
	group, total, ready, newReady, required int
	name, campaign                          string
}

// Called only after the account's readyDrops deduplication. Historical rewards
// contribute to status and counts, but never generate pending notifications.
func (m *Manager) recordReady(acc *parser.Account, drop twitch.Drop, announce bool) {
	m.totalReady.Add(1)
	if m.bot == nil && m.cfg.ManagedAuthAttempts == 0 {
		return
	}
	group := m.accountGroup[acc.Login]
	total := 1
	if group < len(m.groups) {
		total = len(m.groups[group])
	}
	key := rewardBatchKey{group, drop.CampaignID, drop.ID}
	m.rewardMu.Lock()
	if m.rewardBatches == nil {
		m.rewardBatches = make(map[rewardBatchKey]*rewardBatch)
	}
	batch := m.rewardBatches[key]
	if batch == nil {
		batch = &rewardBatch{group: group + 1, total: total, required: drop.Required, name: drop.Name, campaign: drop.CampaignName}
		m.rewardBatches[key] = batch
	}
	batch.ready++
	if announce {
		batch.newReady++
	}
	complete := batch.ready >= batch.total && batch.newReady > 0
	m.rewardMu.Unlock()
	if complete {
		select {
		case m.rewardWake <- struct{}{}:
		default:
		}
	}
}

func summaryLabel(value string) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 100 {
		return string(runes[:99]) + "…"
	}
	return string(runes)
}

func (m *Manager) readySummaries(completeOnly bool) []string {
	m.rewardMu.Lock()
	batches := []rewardBatch{}
	for _, batch := range m.rewardBatches {
		if batch.newReady == 0 || (completeOnly && batch.ready < batch.total) {
			continue
		}
		batches = append(batches, *batch)
		batch.newReady = 0
	}
	m.rewardMu.Unlock()
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].group != batches[j].group {
			return batches[i].group < batches[j].group
		}
		if batches[i].required != batches[j].required {
			return batches[i].required < batches[j].required
		}
		return batches[i].name < batches[j].name
	})
	result := make([]string, 0, len(batches))
	for _, b := range batches {
		state := "Прогресс группы"
		if b.ready >= b.total {
			state = "Награда готова у всей группы"
		}
		hint := "Получение вручную."
		if m.cfg.AutoClaimDrops {
			hint = "Получение проверяется в инвентаре Twitch."
		}
		result = append(result, fmt.Sprintf("🎁 %s\nГруппа %d · %s (цель %d мин)\nГотово: %d/%d · новых: %d\nКампания: %s\n%s Подробности: /status и /logs.", state, b.group, summaryLabel(b.name), b.required, b.ready, b.total, b.newReady, summaryLabel(b.campaign), hint))
	}
	return result
}

func (m *Manager) rewardSummaryLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		completeOnly := false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.rewardWake:
			completeOnly = true
		}
		for _, message := range m.readySummaries(completeOnly) {
			if m.bot != nil && ctx.Err() == nil {
				m.bot.Notify(message)
			}
		}
	}
}
