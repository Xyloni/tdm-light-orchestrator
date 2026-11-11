package orchestrator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

const deadlineReportLead = 3 * time.Hour

type deadlineReport struct {
	total, done, onTrack, atRisk, unavailable int
}

func (s *scheduler) deadlineForecast(key string, g *gameState, c *campaign, now time.Time) deadlineReport {
	coverage := s.coverage(key, g)
	completed, _ := coverage.selectAccounts([]string{c.ID}, "all")
	done := make(map[string]bool, len(completed))
	for _, account := range completed {
		done[account.Credentials.Login] = true
	}
	report := deadlineReport{total: len(coverage.Accounts), done: len(done)}
	active := s.active[key]
	progress := make(map[string]worker.ManagedProgress)
	group := make(map[string]bool, len(c.Group))
	if active != nil && g.Current == c.ID && !active.stopping && !active.groupStarted.IsZero() {
		for _, login := range c.Group {
			group[login] = true
		}
		for _, p := range active.progress {
			progress[p.Login] = p
		}
	} else {
		active = nil
	}
	air := s.airtimeFor(key, c.ID)
	canWatch := !g.Paused && air != nil && len(air.streams) > 0
	wait := time.Duration(0)
	queued := 0
	duration := s.duration(g, c)
	for _, account := range coverage.Accounts {
		login := account.Credentials.Login
		if done[login] {
			continue
		}
		if account.Quarantined || c.Results[login] != "" {
			report.unavailable++
			continue
		}
		if !group[login] {
			queued++
			continue
		}
		eta := s.accountFinishETA(c, progress[login], duration, now)
		wait = max(wait, eta)
		if canWatch && eta <= c.Ends.Sub(now) {
			report.onTrack++
		} else {
			report.atRisk++
		}
	}
	if queued > 0 {
		left := c.Ends.Sub(now) - wait
		slots := len(s.active)
		busy := s.runningAccounts()
		if active != nil {
			slots--
			busy = max(0, busy-len(c.Group))
		}
		if canWatch && slots < s.state.Settings.MaxGames && left >= duration {
			group := planGroup(queued, busy, s.state.Settings.MaxAccounts, left, duration, s.gameSettings(g).SaveResources)
			report.onTrack += min(queued, group*int(left/duration))
		}
		report.atRisk = report.total - report.done - report.onTrack - report.unavailable
	}
	return report
}

type deadlineItem struct {
	key      string
	game     *gameState
	campaign *campaign
}

func (s *scheduler) deadlineItems(now time.Time) []deadlineItem {
	var items []deadlineItem
	for key, g := range s.state.Games {
		if g.Removing {
			continue
		}
		for _, c := range g.Campaigns {
			if (g.SelectedCampaign == "" || c.ID == g.SelectedCampaign) && campaignReady(c, now) && c.Ends.Sub(now) <= deadlineReportLead {
				items = append(items, deadlineItem{key, g, c})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.campaign.Ends.Equal(b.campaign.Ends) {
			return a.campaign.Ends.Before(b.campaign.Ends)
		}
		if a.key != b.key {
			return a.key < b.key
		}
		return a.campaign.ID < b.campaign.ID
	})
	return items
}

func deadlineText(item deadlineItem, r deadlineReport) string {
	g, c := item.game, item.campaign
	text := fmt.Sprintf("«%s» · %s\nДо %s\n\nЗавершили: %d из %d\nПо прогнозу успевают: %d\nПод риском: %d", g.Name, c.Name,
		c.Ends.Local().Format("02.01 15:04 MST"), r.done, r.total, r.onTrack, r.atRisk)
	if r.unavailable > 0 {
		text += fmt.Sprintf("\nКарантин или пропуск: %d", r.unavailable)
	}
	if g.Paused {
		text += "\nИгра на ручной паузе."
	}
	return text + "\n\nПрогноз зависит от эфиров, свободных слотов и темпа начисления."
}

func (s *scheduler) sendDeadlineReports(now time.Time) {
	if s.notify == nil || !s.noticeEnabled("deadline") {
		return
	}
	for _, item := range s.deadlineItems(now) {
		c := item.campaign
		if c.DeadlineReportSentFor.Equal(c.Ends) {
			continue
		}
		report := s.deadlineForecast(item.key, item.game, c, now)
		if report.total == 0 {
			continue
		}
		previous := c.DeadlineReportSentFor
		c.DeadlineReportSentFor = c.Ends
		if !s.persist() {
			c.DeadlineReportSentFor = previous
			return
		}
		s.sendNotice("deadline", "⏰ Отчёт перед дедлайном\n"+deadlineText(item, report)+"\n\nВ Telegram список и выгрузка доступны через /deadline.")
	}
}

func (s *scheduler) telegramDeadline(kind, value string, now time.Time) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "⏰ Отчёты перед дедлайном"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	items := s.deadlineItems(now)
	if kind == "deadline-game" {
		gameRefValue, campaignRef, _ := strings.Cut(value, ":")
		for _, item := range items {
			if gameRef(item.key) == gameRefValue && gameRef(item.campaign.ID) == campaignRef {
				panel.Text = deadlineText(item, s.deadlineForecast(item.key, item.game, item.campaign, now))
				row("📄 Выгрузить завершивших", "cov-deadline:"+value)
				break
			}
		}
		if panel.Text == "" {
			panel.Text = "Кампания завершилась или срок изменился."
		}
		row("К отчётам", "deadline")
		return panel
	}
	panel.Text = "Кампании, до конца которых осталось не больше трёх часов."
	if len(items) == 0 {
		panel.Text = "В ближайшие три часа кампании не заканчиваются."
	}
	page, _ := strconv.Atoi(value)
	page = max(0, min(page, max(0, len(items)-1)/8))
	end := min(len(items), (page+1)*8)
	for _, item := range items[page*8 : end] {
		row(brief(item.game.Name+" · "+item.campaign.Name, 52), "deadline-game:"+gameRef(item.key)+":"+gameRef(item.campaign.ID))
	}
	if page > 0 {
		row("Предыдущие", fmt.Sprintf("deadline:%d", page-1))
	}
	if end < len(items) {
		row("Следующие", fmt.Sprintf("deadline:%d", page+1))
	}
	row("Статус фарма", "status")
	return panel
}
