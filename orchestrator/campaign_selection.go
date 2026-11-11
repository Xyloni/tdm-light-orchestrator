package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func selectableCampaigns(g *gameState, now time.Time) []*campaign {
	var options []*campaign
	for _, c := range g.Campaigns {
		if c.ID == g.SelectedCampaign || (c.Finished == "" && c.Ends.After(now) && !allRewardsExcluded(c)) {
			options = append(options, c)
		}
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Starts.Equal(options[j].Starts) {
			return options[i].ID < options[j].ID
		}
		return options[i].Starts.Before(options[j].Starts)
	})
	return options
}

func (s *scheduler) changeCampaignSelection(key, id string, now time.Time) string {
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра недоступна."
	}
	if id != "" && id != g.SelectedCampaign {
		c := g.Campaigns[id]
		if c == nil || c.Finished != "" || !c.Ends.After(now) || allRewardsExcluded(c) {
			return "Кампания уже недоступна. Обновите список."
		}
	}
	previous := g.SelectedCampaign
	g.SelectedCampaign = id
	if previous != id {
		if !s.persist() {
			g.SelectedCampaign = previous
			return s.notice
		}
		if a := s.active[key]; id != "" && id != g.Current && a != nil && !a.stopping {
			a.stopping = true
			a.process.stop()
		}
		delete(s.retry, key)
		s.event("campaign_selection game=%q campaign_id=%q", g.Name, id)
	}
	if id == "" {
		return "Выбор кампании: авто."
	}
	if c := g.Campaigns[id]; c != nil {
		return "Выбрана кампания «" + c.Name + "»."
	}
	return "Выбор сохранён."
}

func (s *scheduler) telegramCampaignSelection(kind, value string) bot.SettingsPanel {
	ref, selection, _ := strings.Cut(value, ":")
	panel := bot.SettingsPanel{Title: "🎯 Кампания для фарма"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, g := range s.state.Games {
		if g.Removing || gameRef(key) != ref {
			continue
		}
		options := selectableCampaigns(g, now)
		panel.Title += " · " + g.Name
		if kind == "farm-pick" {
			if selection == "auto" {
				panel.Text = s.changeCampaignSelection(key, "", now)
			} else {
				panel.Text = "Кампания уже недоступна. Обновите список."
				for _, c := range options {
					if gameRef(c.ID) == selection {
						panel.Text = s.changeCampaignSelection(key, c.ID, now)
						break
					}
				}
			}
			panel.Text += "\n\n"
		}
		panel.Text += "В режиме «Авто» оркестратор сам выбирает кампанию. При ручном выборе игра ждёт эфиры выбранной кампании."
		if len(options) == 0 {
			panel.Text += "\n\nКампаний пока нет. Обновите список после опроса Twitch."
		}
		auto := "Авто"
		if g.SelectedCampaign == "" {
			auto = "✓ " + auto
		}
		row(auto, "farm-pick:"+ref+":auto")
		for _, c := range options {
			label := brief(c.Name, 44)
			if c.ID == g.SelectedCampaign {
				label = "✓ " + label
			}
			if c.Starts.After(now) {
				label += " · " + c.Starts.Local().Format("02.01")
			}
			row(label, "farm-pick:"+ref+":"+gameRef(c.ID))
		}
		row("Обновить", "farm-campaign:"+ref)
		row("К игре", "game:"+ref)
		return panel
	}
	panel.Text = "Игра недоступна."
	row("К списку игр", "games")
	return panel
}

func (s *scheduler) campaignSelectionCommand(input string, now time.Time) string {
	name, selection, hasSelection := strings.Cut(input, "|")
	key := strings.ToLower(strings.TrimSpace(name))
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра недоступна."
	}
	if !hasSelection {
		current := "Авто"
		if selected := g.Campaigns[g.SelectedCampaign]; selected != nil {
			current = selected.Name
		}
		var out strings.Builder
		fmt.Fprintf(&out, "Текущий выбор: %s\nКампании для выбора:", current)
		for _, c := range selectableCampaigns(g, now) {
			fmt.Fprintf(&out, "\n• %s (%s)", brief(c.Name, 70), c.ID)
		}
		out.WriteString("\n\nДля изменения укажите: игра | ID кампании или auto.")
		return out.String()
	}
	selection = strings.TrimSpace(selection)
	if strings.EqualFold(selection, "auto") {
		return s.changeCampaignSelection(key, "", now)
	}
	for _, c := range selectableCampaigns(g, now) {
		if selection == c.ID {
			return s.changeCampaignSelection(key, c.ID, now)
		}
	}
	return "Кампания не найдена. Укажите её ID из списка."
}
