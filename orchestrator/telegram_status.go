package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *scheduler) telegramStatus(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out strings.Builder
	cfg := s.state.Settings
	free, quarantined := 0, 0
	for _, a := range s.state.Accounts {
		if a.Quarantined {
			quarantined++
		}
		if a.Game == "" && !a.Quarantined {
			free++
		}
	}
	fmt.Fprintf(&out, "📊 Загрузка\nИгры в работе: %d / %d\nАккаунты в работе: %d / %d\nСвободный пул: %d", len(s.active), cfg.MaxGames, s.runningAccounts(), cfg.MaxAccounts, free)
	if quarantined > 0 {
		fmt.Fprintf(&out, "\nКарантин: %d", quarantined)
	}
	fmt.Fprintf(&out, "\n\n⚙️ Настройки фарма\nРежим: %s\nЗапас: %d мин · стартовая скорость: %d%%\nПоиск кампаний: каждые %d сек.", cfg.modeName(), cfg.ReserveMinutes, cfg.ExpectedRatePercent, cfg.DiscoverySeconds)
	fmt.Fprint(&out, "\n\n🟣 Связь с Twitch")
	health := s.state.Catalog
	if health.LastSuccess.IsZero() {
		fmt.Fprint(&out, "\nПоследний успешный опрос: пока не было")
	} else {
		fmt.Fprintf(&out, "\nПоследний успешный опрос: %s", health.LastSuccess.Local().Format("02.01 · 15:04 MST"))
	}
	if health.Failures > 0 {
		fmt.Fprintf(&out, "\nНеудачных опросов подряд: %d", health.Failures)
	}
	if s.discoveryError != "" {
		fmt.Fprint(&out, "\nℹ️ "+s.discoveryError)
	}
	if s.notice != "" {
		fmt.Fprint(&out, "\n⚠️ "+s.notice)
	}
	fmt.Fprint(&out, "\n\n🎮 Игры")
	keys := make([]string, 0, len(s.state.Games))
	for key, g := range s.state.Games {
		if !g.Paused || g.Removing {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := s.active[keys[i]] != nil, s.active[keys[j]] != nil
		if a != b {
			return a
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		if len(s.state.Games) == 0 {
			fmt.Fprint(&out, "\nИгр пока нет. Добавьте игру в разделе «Игры».")
		} else {
			fmt.Fprint(&out, "\nВсе игры на ручной паузе.")
		}
	}
	for _, key := range keys[:min(6, len(keys))] {
		g := s.state.Games[key]
		fmt.Fprintf(&out, "\n\n• %s\n%s", brief(g.Name, 80), s.queueSummary(key, g, now))
		if c := g.Campaigns[g.Current]; c != nil && campaignReady(c, now) && s.active[key] != nil {
			fmt.Fprintf(&out, "\n%s · до %s", brief(c.Name, 75), c.Ends.Local().Format("02.01 15:04"))
		}
	}
	if len(keys) > 6 {
		fmt.Fprintf(&out, "\n\nЕщё игр: %d. Полный список — по кнопке «Игры».", len(keys)-6)
	}
	return out.String()
}
