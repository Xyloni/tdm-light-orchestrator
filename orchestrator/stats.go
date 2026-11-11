package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

const statsDayLayout = "2006-01-02"

type totals struct{ Minutes, Rewards, Campaigns, Groups int64 }

func (t *totals) add(other totals) {
	t.Minutes += other.Minutes
	t.Rewards += other.Rewards
	t.Campaigns += other.Campaigns
	t.Groups += other.Groups
}

type rewardWatermark struct {
	ClaimError      string `json:",omitempty"`
	Minutes         int
	Known, Ready    bool
	Earned, Claimed bool
}

type statistics struct {
	DailyQuarantine map[string]map[string]bool
	DailyRewards    map[string]map[string]int64
	StartedAt       time.Time
	Days            map[string]map[string]totals
	Progress        map[string]rewardWatermark
	Completed       map[string]bool
}

func (s *statistics) credit(day, game string, delta totals) {
	if s.Days == nil {
		s.Days = make(map[string]map[string]totals)
	}
	if s.Days[day] == nil {
		s.Days[day] = make(map[string]totals)
	}
	current := s.Days[day][game]
	current.add(delta)
	s.Days[day][game] = current
}

func (s *statistics) recordCampaign(game string, c *campaign, day string) {
	if c.Finished != "завершена" && c.Finished != "завершена с пропусками" {
		return
	}
	key := game + "\x00" + c.ID
	if s.Completed[key] {
		return
	}
	if s.Completed == nil {
		s.Completed = make(map[string]bool)
	}
	s.Completed[key] = true
	s.credit(day, game, totals{Campaigns: 1})
}

func (s *statistics) recordProgress(game, campaign string, items []worker.ManagedProgress, now time.Time) bool {
	if s.Progress == nil {
		s.Progress = make(map[string]rewardWatermark)
	}
	day := now.Local().Format(statsDayLayout)
	var delta totals
	changed := false
	for _, item := range items {
		if item.UserID == "" {
			continue
		}
		for _, sample := range item.RewardProgress {
			if sample.ID == "" {
				continue
			}
			key := game + "\x00" + campaign + "\x00" + item.UserID + "\x00" + sample.ID
			previous, exists := s.Progress[key]
			next := previous
			next.Earned = previous.Earned || sample.Earned || sample.Ready || sample.Claimed
			next.Claimed = previous.Claimed || sample.Claimed
			if next.Claimed {
				next.ClaimError = ""
			} else if sample.ClaimError != "" {
				next.ClaimError = sample.ClaimError
			}
			if sample.Known {
				baseline := sample.First
				if previous.Known {
					baseline = previous.Minutes
				}
				next.Known = true
				delta.Minutes += int64(max(0, sample.Minutes-baseline))
				next.Minutes = max(previous.Minutes, sample.Minutes)
			}
			if sample.Ready && !previous.Ready {
				delta.Rewards++
				if sample.Name != "" {
					if s.DailyRewards == nil {
						s.DailyRewards = make(map[string]map[string]int64)
					}
					if s.DailyRewards[day] == nil {
						s.DailyRewards[day] = make(map[string]int64)
					}
					s.DailyRewards[day][sample.Name]++
				}
				next.Ready = true
			}
			if !exists || next != previous {
				s.Progress[key] = next
				changed = true
			}
		}
	}
	if delta != (totals{}) {
		s.credit(day, game, delta)
	}
	return changed
}

func (s *statistics) sum(game string, from string) totals {
	var total totals
	for day, games := range s.Days {
		if from != "" && day < from {
			continue
		}
		if game != "" {
			total.add(games[game])
			continue
		}
		for _, value := range games {
			total.add(value)
		}
	}
	return total
}

func (s *scheduler) initializeStats(now time.Time) {
	stats := &s.state.Stats
	if !stats.StartedAt.IsZero() {
		return
	}
	stats.StartedAt = now
	for key, game := range s.state.Games {
		for _, campaign := range game.Campaigns {
			stats.recordCampaign(key, campaign, "")
		}
	}
	s.persist()
}

func (s *scheduler) statsText(filter string, now time.Time) string {
	game := strings.ToLower(strings.TrimSpace(filter))
	title := "Статистика оркестратора"
	if game != "" {
		title += " · " + strings.TrimSpace(filter)
	}
	stats := &s.state.Stats
	local := now.Local()
	line := func(label string, t totals) string {
		return fmt.Sprintf("%s: %d аккаунто-мин · %d готовых наград · %d кампаний · %d групп", label, t.Minutes, t.Rewards, t.Campaigns, t.Groups)
	}
	text := strings.Join([]string{title, line("Сегодня", stats.sum(game, local.Format(statsDayLayout))),
		line("За 7 календарных дней", stats.sum(game, local.AddDate(0, 0, -6).Format(statsDayLayout))),
		line("За всё время", stats.sum(game, ""))}, "\n")
	if !stats.StartedAt.IsZero() {
		text += "\nУчёт минут и наград с " + stats.StartedAt.Local().Format("02.01.2006 15:04 MST") + "."
	}
	return text
}

// telegramStats is called with the scheduler locked.
func (s *scheduler) telegramStats(game string, now time.Time) string {
	stats := &s.state.Stats
	local := now.Local()
	var out strings.Builder
	for _, period := range []struct{ label, from string }{
		{"📅 Сегодня", local.Format(statsDayLayout)},
		{"🗓 Последние 7 дней", local.AddDate(0, 0, -6).Format(statsDayLayout)},
		{"🏁 За всё время", ""},
	} {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(period.label)
		t := stats.sum(game, period.from)
		if t == (totals{}) {
			out.WriteString("\nПока нет результатов.")
			continue
		}
		fmt.Fprintf(&out, "\n⏱ %d мин Twitch · 🎁 %d наград готово\n🏆 %d кампаний завершено · 👥 %d групп", t.Minutes, t.Rewards, t.Campaigns, t.Groups)
	}
	out.WriteString("\n\nℹ️ Минуты суммируются по аккаунтам.")
	if !stats.StartedAt.IsZero() {
		fmt.Fprintf(&out, "\nУчёт минут и наград с %s.", stats.StartedAt.Local().Format("02.01.2006 15:04 MST"))
	}
	return out.String()
}
