package orchestrator

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

type campaignExportSelection struct {
	game, mode string
	ids        []string
	page       int
	until      time.Time
}

func (s *scheduler) telegramCoveragePanel(kind, value string) bot.SettingsPanel {
	ref, arg, _ := strings.Cut(value, ":")
	panel := bot.SettingsPanel{Title: "🏆 Кампании в пуле"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ""
	for candidate := range s.state.Games {
		if gameRef(candidate) == ref {
			key = candidate
			break
		}
	}
	game := s.state.Games[key]
	if game == nil || game.Removing {
		panel.Text = "Игра недоступна."
		row("К списку игр", "games")
		return panel
	}
	panel.Title += " · " + game.Name
	coverage := s.coverage(key, game)
	if kind == "cov" || kind == "cov-sets" {
		page, _ := strconv.Atoi(arg)
		count := len(coverage.Campaigns)
		if kind == "cov-sets" {
			count = len(coverage.Sets)
		}
		page = max(0, min(page, max(0, count-1)/6))
		from, to := page*6, min(count, (page+1)*6)
		var out strings.Builder
		fmt.Fprintf(&out, "Аккаунты в пуле: %d", len(coverage.Accounts))
		if count == 0 {
			out.WriteString("\n\nПодтверждённых результатов пока нет.")
		}
		if kind == "cov" {
			for _, c := range coverage.Campaigns[from:to] {
				fmt.Fprintf(&out, "\n\n🏆 %s\nЗавершили: %d / %d\nГотово к получению: %d · Получено: %d", brief(c.Name, 90), c.Complete, len(coverage.Accounts), c.Ready, c.Claimed)
				if c.Unknown > 0 {
					fmt.Fprintf(&out, "\nСтатус получения неизвестен: %d", c.Unknown)
				}
			}
			row("По наборам кампаний", "cov-sets:"+ref)
		} else {
			for _, set := range coverage.Sets[from:to] {
				fmt.Fprintf(&out, "\n\n👥 %d аккаунтов\n%s", set.Count, brief(set.Names, 210))
			}
			row("По отдельным кампаниям", "cov:"+ref)
		}
		panel.Text = out.String() + "\n\nℹ️ По сохранённым результатам Twitch."
		if page > 0 {
			row("Предыдущие", fmt.Sprintf("%s:%s:%d", kind, ref, page-1))
		}
		if to < count {
			row("Следующие", fmt.Sprintf("%s:%s:%d", kind, ref, page+1))
		}
		if len(coverage.Campaigns) > 0 {
			row("📄 Выгрузить по кампаниям", "cov-export:"+ref)
		}
		row("К игре", "game:"+ref)
		return panel
	}

	now := time.Now()
	selection := &s.telegramCoverage
	if kind == "cov-export" {
		*selection = campaignExportSelection{game: key, mode: "all", until: now.Add(5 * time.Minute)}
	} else if selection.game != key || !now.Before(selection.until) {
		panel.Text = "Срок выбора истёк. Откройте выгрузку заново."
		row("Начать заново", "cov-export:"+ref)
		row("К кампаниям", "cov:"+ref)
		return panel
	}
	switch kind {
	case "cov-toggle":
		for _, c := range coverage.Campaigns {
			if gameRef(c.ID) == arg {
				if i := slices.Index(selection.ids, c.ID); i >= 0 {
					selection.ids = slices.Delete(selection.ids, i, i+1)
				} else {
					selection.ids = append(selection.ids, c.ID)
				}
				break
			}
		}
	case "cov-mode":
		if arg == "all" || arg == "any" || arg == "exact" {
			selection.mode = arg
		}
	case "cov-page":
		selection.page, _ = strconv.Atoi(arg)
	}
	if kind == "cov-confirm" || kind == "cov-pairs" {
		format := exportFull
		if kind == "cov-pairs" {
			format = exportLoginPassword
		}
		accounts, err := coverage.selectAccounts(selection.ids, selection.mode)
		if err != nil || len(accounts) == 0 {
			panel.Text = "Состав выборки изменился. Откройте выгрузку заново."
		} else if data, err := encodeAccountFile(accounts, format); err != nil {
			panel.Text = "Не удалось собрать файл. " + err.Error() + "."
		} else if arg != campaignExportFingerprint(data) {
			panel.Text = "Состав выборки изменился. Проверьте новый результат перед выгрузкой."
		} else {
			var names []string
			for _, c := range coverage.Campaigns {
				if slices.Contains(selection.ids, c.ID) {
					names = append(names, c.Name)
				}
			}
			panel.Document = &bot.TelegramDocument{Name: accountExportName(format, game.Name, names...), Data: data}
			row("К кампаниям", "cov:"+ref)
			return panel
		}
		panel.Text += "\n\n"
	}

	panel.Title = "📄 Выгрузка по кампаниям · " + game.Name
	selection.page = max(0, min(selection.page, max(0, len(coverage.Campaigns)-1)/6))
	from, to := selection.page*6, min(len(coverage.Campaigns), (selection.page+1)*6)
	modeName := campaignExportModeLabel(selection.mode)
	panel.Text += fmt.Sprintf("Выбрано кампаний: %d\nУсловие: %s", len(selection.ids), modeName)
	if accounts, err := coverage.selectAccounts(selection.ids, selection.mode); err == nil {
		panel.Text += fmt.Sprintf("\nПодходят: %d / %d", len(accounts), len(coverage.Accounts))
		if len(accounts) > 0 {
			for _, option := range []struct {
				format        accountExportFormat
				label, action string
			}{{exportFull, "📄 Скачать полный TXT", "cov-confirm"}, {exportLoginPassword, "🔑 Скачать логин:пароль", "cov-pairs"}} {
				if data, err := encodeAccountFile(accounts, option.format); err == nil {
					row(option.label, option.action+":"+ref+":"+campaignExportFingerprint(data))
				} else {
					panel.Text += "\n⚠️ " + option.label + " · " + err.Error() + "."
				}
			}
		}
	}
	panel.Text += "\n\nОтметьте кампании и выберите условие. Файл содержит данные доступа Twitch."
	for _, c := range coverage.Campaigns[from:to] {
		mark := "○ "
		if slices.Contains(selection.ids, c.ID) {
			mark = "✓ "
		}
		row(fmt.Sprintf("%s%s · %d", mark, brief(c.Name, 42), c.Complete), "cov-toggle:"+ref+":"+gameRef(c.ID))
	}
	if from > 0 {
		row("Предыдущие", fmt.Sprintf("cov-page:%s:%d", ref, selection.page-1))
	}
	if to < len(coverage.Campaigns) {
		row("Следующие", fmt.Sprintf("cov-page:%s:%d", ref, selection.page+1))
	}
	for _, mode := range []string{"all", "any", "exact"} {
		mark := "○ "
		if mode == selection.mode {
			mark = "✓ "
		}
		row(mark+campaignExportModeLabel(mode), "cov-mode:"+ref+":"+mode)
	}
	row("К кампаниям", "cov:"+ref)
	return panel
}
