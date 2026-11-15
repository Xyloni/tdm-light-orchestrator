package orchestrator

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

type lotPreviewSelection struct {
	Game      string
	Campaigns []string
}

func validLotRef(ref string) bool {
	if len(ref) != 24 {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

func (s *scheduler) telegramLotPreview(kind, value string) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "Предпросмотр лота"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	if kind == "lot-preview" {
		panel.Text = fmt.Sprintf("Сохранено привязок: %d. Открой лот в Cardinal и скопируй его код.", len(s.state.LotPreviews))
		row("Привязать или изменить лот", "prompt:lot-link")
		row("В меню", "menu")
		return panel
	}
	if kind == "lot-preview-link" || kind == "lot-preview-games" {
		if kind == "lot-preview-link" {
			s.lotPreviewRef = value
			s.lotPreviewDraft = lotPreviewSelection{}
			value = ""
		}
		if !validLotRef(s.lotPreviewRef) {
			panel.Text = "Открой привязку заново."
			row("К привязкам", "lot-preview")
			return panel
		}
		saved := s.state.LotPreviews[s.lotPreviewRef]
		panel.Text = "Выберите игру для лота " + s.lotPreviewRef + "."
		if len(saved.Campaigns) > 0 {
			panel.Text += fmt.Sprintf("\nСохранено: %s · кампаний: %d.", saved.Game, len(saved.Campaigns))
		}
		var keys []string
		for key, g := range s.state.Games {
			if !g.Removing {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		page, _ := strconv.Atoi(value)
		page = max(0, min(page, max(0, len(keys)-1)/8))
		end := min(len(keys), (page+1)*8)
		for _, key := range keys[page*8 : end] {
			row(s.state.Games[key].Name, "lot-preview-game:"+gameRef(key))
		}
		if page > 0 {
			row("Предыдущая", fmt.Sprintf("lot-preview-games:%d", page-1))
		}
		if end < len(keys) {
			row("Следующая", fmt.Sprintf("lot-preview-games:%d", page+1))
		}
		row("В меню", "menu")
		return panel
	}
	ref, arg, _ := strings.Cut(value, ":")
	for key, g := range s.state.Games {
		if gameRef(key) != ref || g.Removing {
			continue
		}
		if kind == "lot-preview-game" {
			s.lotPreviewDraft = lotPreviewSelection{Game: g.Name}
			if saved := s.state.LotPreviews[s.lotPreviewRef]; saved.Game == g.Name {
				s.lotPreviewDraft.Campaigns = slices.Clone(saved.Campaigns)
			}
		}
		draft := &s.lotPreviewDraft
		if !validLotRef(s.lotPreviewRef) || draft.Game != g.Name {
			break
		}
		pageText, campaignRef, _ := strings.Cut(arg, ":")
		if kind == "lot-toggle" {
			for _, c := range g.Campaigns {
				if gameRef(c.ID) != campaignRef || len(c.Rewards) == 0 {
					continue
				}
				if i := slices.Index(draft.Campaigns, c.ID); i >= 0 {
					draft.Campaigns = slices.Delete(draft.Campaigns, i, i+1)
				} else if len(draft.Campaigns) < 20 {
					draft.Campaigns = append(draft.Campaigns, c.ID)
				}
			}
		}
		if kind == "lot-preview-save" && len(draft.Campaigns) > 0 {
			if s.state.LotPreviews == nil {
				s.state.LotPreviews = make(map[string]lotPreviewSelection)
			}
			previous, existed := s.state.LotPreviews[s.lotPreviewRef]
			s.state.LotPreviews[s.lotPreviewRef] = lotPreviewSelection{Game: g.Name, Campaigns: slices.Clone(draft.Campaigns)}
			if s.persist() {
				panel.Text = "Выбор сохранён.\n\n"
			} else {
				if existed {
					s.state.LotPreviews[s.lotPreviewRef] = previous
				} else {
					delete(s.state.LotPreviews, s.lotPreviewRef)
				}
				panel.Text = s.notice + "\n\n"
			}
		}
		panel.Text += fmt.Sprintf("%s\nВыбрано кампаний: %d / 20.", g.Name, len(draft.Campaigns))
		var ids []string
		for id, c := range g.Campaigns {
			if len(c.Rewards) > 0 {
				ids = append(ids, id)
			}
		}
		slices.SortFunc(ids, func(a, b string) int { return strings.Compare(g.Campaigns[a].Name+" "+a, g.Campaigns[b].Name+" "+b) })
		page, _ := strconv.Atoi(pageText)
		page = max(0, min(page, max(0, len(ids)-1)/8))
		end := min(len(ids), (page+1)*8)
		for _, id := range ids[page*8 : end] {
			c := g.Campaigns[id]
			label := brief(c.Name, 48)
			if slices.Contains(draft.Campaigns, id) {
				label = "✓ " + label
			}
			row(label, fmt.Sprintf("lot-toggle:%s:%d:%s", ref, page, gameRef(c.ID)))
		}
		if page > 0 {
			row("Предыдущая", fmt.Sprintf("lot-preview-page:%s:%d", ref, page-1))
		}
		if end < len(ids) {
			row("Следующая", fmt.Sprintf("lot-preview-page:%s:%d", ref, page+1))
		}
		if len(draft.Campaigns) > 0 {
			row("Сохранить выбор", fmt.Sprintf("lot-preview-save:%s:%d", ref, page))
		}
		row("К выбору игры", "lot-preview-games")
		return panel
	}
	panel.Text = "Откройте выбор игры заново."
	row("К выбору игры", "lot-preview")
	return panel
}
