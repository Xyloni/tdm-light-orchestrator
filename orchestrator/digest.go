package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type digestSettings struct{ At, LastDay string }

func (s *scheduler) setDigest(value string) string {
	if value == "" {
		if s.state.Digest.At == "" {
			return "Суточная сводка выключена. Укажите время HH:MM или off."
		}
		return "Суточная сводка: " + s.state.Digest.At + " по времени сервера. Для отключения укажите off."
	}
	if value == "off" {
		value = ""
	} else if parsed, err := time.Parse("15:04", value); err != nil || parsed.Format("15:04") != value {
		return "Укажите время в формате HH:MM или off."
	}
	old := s.state.Digest.At
	s.state.Digest.At = value
	if !s.persist() {
		s.state.Digest.At = old
		return s.notice
	}
	if value == "" {
		return "Суточная сводка выключена."
	}
	return "Суточная сводка назначена на " + value + " по времени сервера. Она охватывает предыдущий календарный день."
}

func (s *scheduler) sendDailyDigest(now time.Time) {
	local := now.Local()
	today := local.Format(statsDayLayout)
	d := &s.state.Digest
	if d.At == "" || d.LastDay == today || local.Format("15:04") < d.At || s.state.NotificationsMuted || s.notify == nil || len(s.configuredBots()) == 0 {
		return
	}
	message := s.dailyDigest(local)
	old := d.LastDay
	d.LastDay = today
	if !s.persist() {
		d.LastDay = old
		return
	}
	s.notify(message)
}

func (s *scheduler) dailyDigest(local time.Time) string {
	day := local.AddDate(0, 0, -1).Format(statsDayLayout)
	var total totals
	for _, counts := range s.state.Stats.Days[day] {
		total.add(counts)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Суточная сводка · %s\n%d аккаунто-мин · %d готовых наград · %d завершённых кампаний · %d групп", day, total.Minutes, total.Rewards, total.Campaigns, total.Groups)
	type namedCount struct {
		name  string
		count int64
	}
	var rewards []namedCount
	for name, count := range s.state.Stats.DailyRewards[day] {
		rewards = append(rewards, namedCount{name, count})
	}
	sort.Slice(rewards, func(i, j int) bool {
		if rewards[i].count != rewards[j].count {
			return rewards[i].count > rewards[j].count
		}
		return rewards[i].name < rewards[j].name
	})
	if len(rewards) > 0 {
		fmt.Fprint(&out, "\nГотовые награды:")
		for _, item := range rewards[:min(3, len(rewards))] {
			fmt.Fprintf(&out, "\n• %s ×%d", brief(item.name, 55), item.count)
		}
	}
	quarantine := 0
	var recent []string
	for _, a := range s.state.Accounts {
		if !a.Quarantined {
			continue
		}
		quarantine++

	}
	for login := range s.state.Stats.DailyQuarantine[day] {
		recent = append(recent, login)
	}
	sort.Strings(recent)
	fmt.Fprintf(&out, "\nКарантин: %d сейчас; за отчётный день добавлено %d.", quarantine, len(recent))
	if len(recent) > 0 {
		fmt.Fprint(&out, " ", strings.Join(recent[:min(3, len(recent))], ", "))
	}
	type ending struct {
		label string
		at    time.Time
	}
	var soon []ending
	for _, g := range s.state.Games {
		if g.Removing {
			continue
		}
		for _, c := range g.Campaigns {
			if c.Finished == "" && !allRewardsExcluded(c) && c.Ends.After(local) && c.Ends.Sub(local) <= 24*time.Hour {
				soon = append(soon, ending{brief(g.Name, 32) + " · " + brief(c.Name, 40), c.Ends})
			}
		}
	}
	sort.Slice(soon, func(i, j int) bool { return soon[i].at.Before(soon[j].at) })
	if len(soon) > 0 {
		fmt.Fprint(&out, "\nЗаканчиваются в ближайшие 24 ч:")
		for _, c := range soon[:min(3, len(soon))] {
			fmt.Fprintf(&out, "\n• %s — %s", c.label, c.at.Local().Format("02.01 15:04"))
		}
		if len(soon) > 3 {
			fmt.Fprintf(&out, "\nЕщё кампаний: %d.", len(soon)-3)
		}
	} else {
		fmt.Fprint(&out, "\nКампаний с дедлайном в ближайшие 24 ч нет.")
	}
	return out.String()
}

func brief(s string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}
