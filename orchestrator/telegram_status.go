package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// withoutActiveCampaign is used by both Telegram status lists while s.mu is held.
func (s *scheduler) withoutActiveCampaign(key string, g *gameState, now time.Time) bool {
	return !g.Paused && !g.Removing && !g.Regroup && s.active[key] == nil && s.queuedCampaign(g, now) == nil
}

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
		if (!g.Paused || g.Removing) && !s.withoutActiveCampaign(key, g, now) {
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
			fmt.Fprint(&out, "\nИгры на паузе и без активной кампании доступны по отдельным кнопкам.")
		}
	}
	for _, key := range keys[:min(6, len(keys))] {
		fmt.Fprint(&out, "\n\n"+s.telegramGameStatus(key, now))
	}
	if len(keys) > 6 {
		fmt.Fprintf(&out, "\n\nЕщё игр: %d. Полный список — по кнопке «Игры».", len(keys)-6)
	}
	return out.String()
}

func (s *scheduler) telegramGameStatus(key string, now time.Time) string {
	g := s.state.Games[key]
	a := s.active[key]
	summary := s.queueSummary(key, g, now)
	c := s.queuedCampaign(g, now)
	if a != nil {
		c = g.Campaigns[g.Current]
	}
	heading := "🎮 " + brief(g.Name, 80)
	if c == nil || !campaignReady(c, now) || g.Removing || g.Regroup || (a != nil && a.stopping) {
		return heading + "\n" + summary
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%s\n🏆 %s\nДо: %s", heading, brief(c.Name, 85), c.Ends.Local().Format("02.01 в 15:04 MST"))
	summary = strings.TrimPrefix(summary, "Кампания: "+c.Name+"\n")
	air := s.airtimeFor(key, c.ID)
	if air != nil && !air.at.IsZero() {
		if before, after, found := strings.Cut(summary, air.summary()); found {
			var lines []string
			for _, line := range strings.Split(air.status(), "\n") {
				if strings.HasPrefix(line, "В каталоге категории тоже нет") ||
					strings.HasPrefix(line, "Поиск продолжится на следующих страницах") {
					continue
				}
				lines = append(lines, line)
			}
			if len(air.streams) > 0 {
				lines = []string{fmt.Sprintf("Эфиров с Drops: %d", len(air.streams))}
			}
			summary = before + strings.Join(lines, "\n") + after + "\n🕒 Проверено в " + air.at.Local().Format("15:04 MST")
		}
	}
	summary = strings.Replace(summary, "Активная группа: ", "В группе: ", 1)
	summary = strings.Replace(summary, "\nВ очереди: ", " · В очереди: ", 1)
	fmt.Fprint(&out, "\n"+summary)
	return out.String()
}
