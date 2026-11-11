package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *scheduler) gameSettings(g *gameState) settings {
	cfg := s.state.Settings
	if g.Mode == "speed" || g.Mode == "resources" {
		cfg.SaveResources = g.Mode == "resources"
	}
	if g.ReserveMinutes > 0 {
		cfg.ReserveMinutes = g.ReserveMinutes
	}
	return cfg
}

func (s *scheduler) changeGamePolicy(key, mode, reserve string) string {
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра не найдена."
	}
	minutes, err := strconv.Atoi(reserve)
	if (mode != "" && mode != "speed" && mode != "resources") || err != nil || minutes < 0 || minutes > 1440 {
		return "Режим: default, speed или resources. Запас: 0 для общего значения или 1–1440 минут."
	}
	oldMode, oldReserve := g.Mode, g.ReserveMinutes
	g.Mode, g.ReserveMinutes = mode, minutes
	if !s.persist() {
		g.Mode, g.ReserveMinutes = oldMode, oldReserve
		return s.notice
	}
	return "Правила игры сохранены. Они применятся к следующим группам."
}

func (s *scheduler) setAutoClaim(key string, enabled bool) string {
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра недоступна."
	}
	if g.AutoClaimDrops != enabled {
		g.AutoClaimDrops = enabled
		if !s.persist() {
			g.AutoClaimDrops = !enabled
			return s.notice
		}
		if a := s.active[key]; a != nil && !a.stopping {
			a.stopping = true
			a.process.stop()
		}
	}
	if enabled {
		return "Автополучение включено. Получение проверяется в инвентаре Twitch."
	}
	return "Автополучение выключено."
}

func (s *scheduler) queueSummary(key string, g *gameState, now time.Time) string {
	if g.Removing {
		return "Игра удаляется."
	}
	if g.Paused {
		return "Ожидание: ручная пауза."
	}
	if g.Regroup {
		return "Ожидание: перегруппировка."
	}
	a := s.active[key]
	if a != nil && a.stopping {
		return "Ожидание: остановка предыдущего процесса."
	}
	c := g.Campaigns[g.Current]
	if a == nil {
		c = s.queuedCampaign(g, now)
	}
	if c == nil || !campaignReady(c, now) {
		if g.SelectedCampaign != "" {
			selected := g.Campaigns[g.SelectedCampaign]
			if selected == nil {
				return "Ожидание: выбранная кампания пока недоступна."
			}
			if selected.Starts.After(now) && selected.Finished == "" {
				return fmt.Sprintf("Кампания: %s\nОжидание старта: %s.", selected.Name, selected.Starts.Local().Format("02.01 15:04 MST"))
			}
			return fmt.Sprintf("Кампания: %s\nКампания завершилась или сейчас недоступна. Выберите другую или включите «Авто».", selected.Name)
		}
		var next time.Time
		excluded := false
		for _, item := range g.Campaigns {
			if allRewardsExcluded(item) && !now.Before(item.Starts) && now.Before(item.Ends) {
				excluded = true
			}
			if item.Finished == "" && !allRewardsExcluded(item) && item.Starts.After(now) && (next.IsZero() || item.Starts.Before(next)) {
				next = item.Starts
			}
		}
		if !next.IsZero() {
			return "Ожидание: старт кампании " + next.Local().Format("02.01 15:04 MST") + "."
		}
		if excluded {
			return "Ожидание: все награды доступной кампании исключены."
		}
		return "Ожидание: Twitch пока не показывает активную кампанию для игры."
	}
	if a != nil && !a.groupStarted.IsZero() {
		message := s.groupSummary(key, c, now) + "\n" + s.airtimeFor(key, c.ID).summary()
		for _, progress := range a.progress {
			for _, reward := range progress.RewardProgress {
				if reward.Known && reward.Minutes > 0 {
					return message
				}
			}
		}
		return message + "\nℹ️ Twitch пока не подтвердил минуты группы."
	}
	count := len(pending(s.state, key, c))
	usable, free := 0, 0
	for _, account := range s.state.Accounts {
		if account.Quarantined {
			continue
		}
		if account.Game == key {
			usable++
		}
		if account.Game == "" && c.Results[account.Credentials.Login] == "" {
			free++
		}
	}
	count += min(free, max(0, g.Target-usable))
	if count == 0 {
		return "Ожидание: нет доступных аккаунтов. Проверьте пул и карантин."
	}
	air := s.airtimeFor(key, c.ID)
	message := "Готова к запуску."
	if air == nil || len(air.streams) == 0 {
		message = fmt.Sprintf("Кампания: %s\n%s", c.Name, air.summary())
	} else if now.Before(s.retry[key]) {
		message = "Ожидание: повтор запуска майнера."
	} else if a == nil && len(s.active) >= s.state.Settings.MaxGames {
		message = "Ожидание: свободный слот игры."
	} else {
		duration := s.duration(g, c)
		running, limit := s.runningAccounts(), s.state.Settings.MaxAccounts
		deadline := s.planDeadline(g, c)
		group := planGroup(count, running, limit, deadline.Sub(now), duration, s.gameSettings(g).SaveResources)
		if group > max(0, limit-running) && s.canWaitForCapacity(deadline, count, duration, now) {
			message = "Ожидание: свободный лимит аккаунтов."
		}
	}
	if !now.Before(s.latestStart(g, c, count)) {
		message += "\n⚠️ Риск дедлайна: по прогнозу запаса в рамках общего лимита уже нет."
	}
	return message
}

func (s *scheduler) quarantineAccounts(names []string) bool {
	logins := make(map[string]bool, len(names))
	for _, name := range names {
		logins[name] = true
	}
	changed := false
	for i := range s.state.Accounts {
		a := &s.state.Accounts[i]
		if logins[a.Credentials.Login] && !a.Quarantined {
			a.Quarantined = true
			stats := &s.state.Stats
			day := time.Now().Local().Format(statsDayLayout)
			if stats.DailyQuarantine == nil {
				stats.DailyQuarantine = make(map[string]map[string]bool)
			}
			if stats.DailyQuarantine[day] == nil {
				stats.DailyQuarantine[day] = make(map[string]bool)
			}
			stats.DailyQuarantine[day][a.Credentials.Login] = true
			changed = true
		}
	}
	return changed
}

func (s *scheduler) quarantineSummary(key string) string {
	var names []string
	for _, a := range s.state.Accounts {
		if !a.Quarantined || (key != "" && a.Game != key) {
			continue
		}
		pool := a.Game
		if g := s.state.Games[pool]; g != nil {
			pool = g.Name
		}
		if pool == "" {
			pool = "Свободный пул"
		}
		names = append(names, fmt.Sprintf("%s · %s · ошибка авторизации", a.Credentials.Login, pool))
	}
	if len(names) == 0 {
		return "В карантине нет аккаунтов."
	}
	return fmt.Sprintf("Карантин: %d. Загрузите новый OAuth-токен, чтобы вернуть аккаунт в работу.\n%s", len(names), strings.Join(names, "\n"))
}
