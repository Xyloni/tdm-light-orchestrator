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

type partialGroupExport struct {
	id, key          string
	active           *activeGame
	campaign         *campaign
	started, expires time.Time
	accounts         []account
}

func (s *scheduler) accountFinishETA(c *campaign, p worker.ManagedProgress, fallback time.Duration, now time.Time) time.Duration {
	if p.Required <= 0 {
		return fallback
	}
	rate := s.planningRate(c)
	age := worker.WatchRateMaxAge(s.miner.CampaignPollIntervalSec)
	if freshRate(p.LongRateKnown, p.LongRate, p.LongRateChecked, now, age) {
		rate = max(0.05, p.LongRate)
	} else if freshRate(p.RateKnown, p.Rate, p.RateChecked, now, age) {
		rate = max(0.05, p.Rate)
	}
	return estimate(max(0, p.Required-p.Current)+p.RemainingAfterCurrent, rate)
}

func (s *scheduler) partialGroup(key string) (*activeGame, *campaign) {
	g, a := s.state.Games[key], s.active[key]
	if g == nil || g.Paused || g.Removing || g.Regroup || a == nil || a.stopping || a.removing || a.groupStarted.IsZero() {
		return nil, nil
	}
	return a, g.Campaigns[g.Current]
}

func (s *scheduler) previewPartialExport(ref string, count int, now time.Time) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partialExport = partialGroupExport{}
	panel := bot.SettingsPanel{Title: "📤 Снять часть группы", Buttons: [][]bot.SettingsButton{{{Label: "К пулу", Action: "game-pool:" + ref}}}}
	key := ""
	for candidate := range s.state.Games {
		if gameRef(candidate) == ref {
			key = candidate
			break
		}
	}
	a, c := s.partialGroup(key)
	if c == nil {
		panel.Text = "Сейчас нет активной группы, из которой можно снять аккаунты."
		return panel
	}
	progress := make(map[string]worker.ManagedProgress, len(a.progress))
	for _, p := range a.progress {
		progress[p.Login] = p
	}
	byLogin := make(map[string]account)
	for _, item := range s.state.Accounts {
		if item.Game == key && !item.Quarantined {
			byLogin[item.Credentials.Login] = item
		}
	}
	type candidate struct {
		account account
		eta     time.Duration
	}
	choices := make([]candidate, 0, len(c.Group))
	duration := s.duration(s.state.Games[key], c)
	for _, login := range c.Group {
		item, found := byLogin[login]
		p := progress[login]
		if found && c.Results[login] == "" && !terminal(p.Status) {
			choices = append(choices, candidate{item, s.accountFinishETA(c, p, duration, now)})
		}
	}
	if count < 1 || count > len(choices) {
		panel.Text = fmt.Sprintf("Доступно для снятия: %d. Укажите количество в этом пределах.", len(choices))
		panel.Buttons = append([][]bot.SettingsButton{{{Label: "Ввести количество", Action: "prompt:partial:" + ref}}}, panel.Buttons...)
		return panel
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].eta > choices[j].eta })
	selection := partialGroupExport{
		id: strconv.FormatInt(now.UnixNano(), 36), key: key, active: a, campaign: c,
		started: a.groupStarted, expires: now.Add(2 * time.Minute),
	}
	var names []string
	for i, choice := range choices[:count] {
		selection.accounts = append(selection.accounts, choice.account)
		if i < 5 {
			names = append(names, choice.account.Credentials.Login)
		}
	}
	s.partialExport = selection
	panel.Text = fmt.Sprintf("«%s» · %s\n\nВыбрано: %d из %d незавершивших аккаунтов.\nПервые в списке: %s.\n\nСначала отправим полный TXT. После отправки выбранные аккаунты остановятся и будут удалены из пула игры. Остальные продолжат фарм.", s.state.Games[key].Name, c.Name, count, len(choices), strings.Join(names, ", "))
	panel.Buttons = append([][]bot.SettingsButton{{{Label: "Скачать TXT и снять аккаунты", Action: "partial-confirm:" + selection.id}}}, panel.Buttons...)
	return panel
}

func (s *scheduler) validatePartialExport(selection partialGroupExport) bool {
	a, c := s.partialGroup(selection.key)
	if a == nil || a != selection.active || c != selection.campaign || !a.groupStarted.Equal(selection.started) {
		return false
	}
	members := make(map[string]bool, len(c.Group))
	for _, login := range c.Group {
		members[login] = true
	}
	want := make(map[string]account, len(selection.accounts))
	for _, item := range selection.accounts {
		want[item.Credentials.Login] = item
	}
	matched := 0
	for _, current := range s.state.Accounts {
		if item, found := want[current.Credentials.Login]; found {
			if item != current || !members[current.Credentials.Login] || c.Results[current.Credentials.Login] != "" {
				return false
			}
			matched++
		}
	}
	if matched != len(want) {
		return false
	}
	for _, p := range a.progress {
		if _, found := want[p.Login]; found && terminal(p.Status) {
			return false
		}
	}
	return true
}

func (s *scheduler) confirmPartialExport(id string, now time.Time) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "📤 Снять часть группы", Buttons: [][]bot.SettingsButton{{{Label: "К списку игр", Action: "games"}}}}
	selection := s.partialExport
	if id == "" || selection.id != id || !now.Before(selection.expires) || !s.validatePartialExport(selection) {
		panel.Text = "Состав группы изменился или подтверждение устарело. Откройте выборку заново."
		return panel
	}
	data, err := encodeAccountFile(selection.accounts, exportFull)
	if err != nil {
		panel.Text = "Не удалось собрать TXT. " + err.Error() + "."
		return panel
	}
	s.partialExport = partialGroupExport{}
	g := s.state.Games[selection.key]
	panel.Buttons = [][]bot.SettingsButton{{{Label: "К пулу", Action: "game-pool:" + gameRef(selection.key)}}}
	panel.Document = &bot.TelegramDocument{
		Name: accountExportName(exportFull, g.Name, selection.campaign.Name, "Снятие из группы"),
		Data: data,
		AfterSend: func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.applyPartialExport(selection)
		},
	}
	return panel
}

func (s *scheduler) applyPartialExport(selection partialGroupExport) string {
	if !s.validatePartialExport(selection) {
		return "Снятие отменено: состав группы изменился во время отправки."
	}
	a, c := selection.active, selection.campaign
	if len(a.process.commands) >= cap(a.process.commands) {
		return "Майнер занят. Снятие отменено, аккаунты остались в пуле."
	}
	names := make([]string, 0, len(selection.accounts))
	remove := make(map[string]bool, len(selection.accounts))
	if c.Results == nil {
		c.Results = make(map[string]string)
	}
	for _, item := range selection.accounts {
		login := item.Credentials.Login
		names = append(names, login)
		remove[login] = true
		c.Results[login] = "trimmed"
	}
	previous := s.state.Accounts
	remaining := make([]account, 0, len(previous)-len(names))
	for _, item := range previous {
		if !remove[item.Credentials.Login] {
			remaining = append(remaining, item)
		}
	}
	s.state.Accounts = remaining
	if !s.persist() {
		s.state.Accounts = previous
		for _, login := range names {
			delete(c.Results, login)
		}
		return "Снятие отменено: не удалось сохранить изменения пула."
	}
	// Only scheduler operations send commands, and they all hold s.mu.
	a.process.commands <- minerCommand{Remove: names}
	s.event("group_partial_export game=%q campaign=%q accounts=%d", selection.key, c.ID, len(names))
	return fmt.Sprintf("Аккаунтов удалено из пула: %d. Команда остановки отправлена майнеру.", len(names))
}
