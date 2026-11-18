package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

type lotBundleDraft struct {
	Ref, Token, Game string
	Search           string
	Selected         map[string]map[string]bool
	Discovered       map[string]lotCatalogCampaign
	InventoryIDs     map[string]bool
	InventoryPartial bool
}

// Call with the scheduler lock. Removed entries are only available to lots that already use them.
func (s *scheduler) lotCatalogFor(ref string) map[string]lotCatalogCampaign {
	catalog := s.state.LotCatalog
	var extended map[string]lotCatalogCampaign
	for _, reward := range s.state.ManagedLots[ref].Rewards {
		campaign, archived := s.state.LotArchivedCampaigns[reward.Campaign]
		if _, visible := catalog[reward.Campaign]; visible || !archived {
			continue
		}
		if extended == nil {
			extended = make(map[string]lotCatalogCampaign, len(catalog)+1)
			maps.Copy(extended, catalog)
		}
		extended[reward.Campaign] = campaign
	}
	if extended != nil {
		return extended
	}
	return catalog
}

// Call with the scheduler lock. Keep discoveries in the draft until the operator saves the lot.
func (s *scheduler) lotBundleCatalog(draft *lotBundleDraft) map[string]lotCatalogCampaign {
	known := s.lotCatalogFor(draft.Ref)
	if len(draft.Discovered) == 0 {
		return known
	}
	catalog := maps.Clone(draft.Discovered)
	for id, item := range known {
		if discovered, exists := draft.Discovered[id]; exists {
			item, _ = mergeLotInventoryCampaign(item, discovered)
		}
		catalog[id] = item
	}
	return catalog
}

func (s *scheduler) registerLotTitle(ref, title string, sample []lotSampleAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.state.LotTitles[ref]
	if exists && previous == title {
		s.storeLotSampleLocked(ref, sample)
		return nil
	}
	if s.state.LotTitles == nil {
		s.state.LotTitles = make(map[string]string)
	}
	s.state.LotTitles[ref] = title
	if s.persist() {
		s.storeLotSampleLocked(ref, sample)
		return nil
	}
	if exists {
		s.state.LotTitles[ref] = previous
	} else {
		delete(s.state.LotTitles, ref)
	}
	return fmt.Errorf("не удалось сохранить название лота")
}

func bundleButton(panel *bot.SettingsPanel, label, action string) {
	panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
}

func bundlePageRows(panel *bot.SettingsPanel, value string, items []bot.SettingsButton, action func(int) string) {
	const size = 8
	page, _ := strconv.Atoi(value)
	page = max(0, min(page, max(0, len(items)-1)/size))
	end := min(len(items), (page+1)*size)
	for _, item := range items[page*size : end] {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{item})
	}
	var nav []bot.SettingsButton
	if page > 0 {
		nav = append(nav, bot.SettingsButton{Label: "⬅️ Назад", Action: action(page - 1)})
	}
	if end < len(items) {
		nav = append(nav, bot.SettingsButton{Label: "➡️ Дальше", Action: action(page + 1)})
	}
	if len(nav) > 0 {
		panel.Buttons = append(panel.Buttons, nav)
	}
}

func (s *scheduler) telegramLotSearchResult(token, query string) bot.SettingsPanel {
	s.mu.Lock()
	if draft := s.lotBundleDraft; draft != nil && draft.Token == token {
		draft.Search = query
	}
	s.mu.Unlock()
	return s.telegramLotBundle("lb", token+":matches:0")
}

func (s *scheduler) telegramLotInventory(token string) bot.SettingsPanel {
	found, partial, err := s.scanLotInventory(token)
	s.mu.Lock()
	draft := s.lotBundleDraft
	if err == nil && draft != nil && draft.Token == token {
		if draft.Discovered == nil {
			draft.Discovered = make(map[string]lotCatalogCampaign)
		}
		draft.InventoryIDs = make(map[string]bool, len(found))
		for id, item := range found {
			if previous, exists := draft.Discovered[id]; exists {
				item, _ = mergeLotInventoryCampaign(previous, item)
			}
			draft.Discovered[id] = item
			draft.InventoryIDs[id] = true
		}
		draft.InventoryPartial = partial
	}
	s.mu.Unlock()
	panel := s.telegramLotBundle("lb", token+":inventory:0")
	if err != nil && panel.Title == "🎒 Кампании из инвентаря лота" {
		panel.Text = "⚠️ " + err.Error() + ".\n\n" + panel.Text
	}
	return panel
}

func (s *scheduler) telegramLotBundle(kind, value string) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "📋 Состав лота"}
	if kind == "lb-list" {
		panel.Title = "📋 Составы лотов"
		titles := s.state.LotTitles
		refs := make([]string, 0, len(titles))
		for ref := range titles {
			refs = append(refs, ref)
		}
		slices.SortFunc(refs, func(a, b string) int {
			return strings.Compare(strings.ToLower(titles[a])+a, strings.ToLower(titles[b])+b)
		})
		var items []bot.SettingsButton
		for _, ref := range refs {
			status := "Нужен состав"
			if len(s.state.ManagedLots[ref].Rewards) > 0 {
				status = "Состав сохранён"
			}
			items = append(items, bot.SettingsButton{Label: status + " · " + brief(titles[ref], 44), Action: "lb-open:" + ref})
		}
		panel.Text = "Выберите лот, чтобы задать набор наград для проверки в Cardinal."
		if len(refs) == 0 {
			panel.Text = "Чтобы добавить лот, начните подключение в боте Cardinal."
		}
		bundlePageRows(&panel, value, items, func(page int) string { return fmt.Sprintf("lb-list:%d", page) })
		bundleButton(&panel, "🔄 Обновить список", "lb-list:0")
		bundleButton(&panel, "🏷 К лотам", "managed-lots")
		return panel
	}
	if kind == "lb-start" {
		value = strings.ToLower(value)
		if !validLotRef(value) || s.state.LotTitles[value] == "" {
			panel.Text = "Лот ещё не добавлен. Вернитесь в Cardinal и продолжите подключение из карточки лота."
			bundleButton(&panel, "⬅️ К составам лотов", "lb-list:0")
			return panel
		}
		kind = "lb-open"
	}
	if kind == "lb-open" && validLotRef(value) {
		var key [6]byte
		if _, err := rand.Read(key[:]); err != nil {
			panel.Text = "Не удалось открыть редактор. Попробуйте ещё раз."
			bundleButton(&panel, "⬅️ К списку лотов", "lb-list:0")
			return panel
		}
		saved := s.state.ManagedLots[value]
		draft := &lotBundleDraft{Ref: value, Token: hex.EncodeToString(key[:]), Game: strings.ToLower(saved.Game), Selected: make(map[string]map[string]bool)}
		for _, reward := range saved.Rewards {
			if draft.Selected[reward.Campaign] == nil {
				draft.Selected[reward.Campaign] = make(map[string]bool)
			}
			draft.Selected[reward.Campaign][reward.ID] = true
		}
		s.lotBundleDraft = draft
		kind, value = "lb", draft.Token+":games:0"
		if draft.Game != "" {
			value = draft.Token + ":campaigns:0"
		}
	}
	args := strings.Split(value, ":")
	draft := s.lotBundleDraft
	if draft == nil || len(args) < 2 || args[0] != draft.Token {
		panel.Text = "Редактор закрыт. Откройте лот заново."
		bundleButton(&panel, "⬅️ К списку лотов", "lb-list:0")
		return panel
	}
	lotTitle := s.state.LotTitles[draft.Ref]
	if lotTitle == "" {
		lotTitle = "Лот · " + draft.Ref
	}
	draftHeader := func() string {
		header := lotTitle + "\n\n"
		if saved := len(s.state.ManagedLots[draft.Ref].Rewards); saved > 0 {
			header += fmt.Sprintf("Сохранённый состав · наград %d\n", saved)
		}
		selectedCount := 0
		for _, rewards := range draft.Selected {
			selectedCount += len(rewards)
		}
		return header + fmt.Sprintf("Черновик · выбрано %d\n\n", selectedCount)
	}
	panel.Text = draftHeader()
	link := func(parts ...string) string { return "lb:" + draft.Token + ":" + strings.Join(parts, ":") }
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	view := args[1]
	catalog := s.lotBundleCatalog(draft)
	findCampaign := "prompt:lb-find:" + draft.Token
	scanInventory := "lb-scan:" + draft.Token
	if view == "tools" {
		panel.Title = "⚙️ Состав лота · дополнительно"
		bundleButton(&panel, "🎒 Из инвентаря лота", scanInventory)
		if draft.Search != "" {
			bundleButton(&panel, "⬅️ К результатам поиска", link("matches", "0"))
		}
		if draft.Game == "" {
			bundleButton(&panel, "⬅️ К выбору игры", link("games", "0"))
		} else {
			bundleButton(&panel, "⬅️ К кампаниям", link("campaigns", "0"))
		}
		return panel
	}
	if view == "matches" || view == "inventory" {
		inventory := view == "inventory"
		if inventory {
			panel.Title = "🎒 Кампании из инвентаря лота"
			panel.Text += "Выберите игру и кампанию по результатам проверки одного аккаунта."
		} else {
			panel.Title = "🔎 Результаты поиска"
			panel.Text += "Запрос · " + brief(draft.Search, 100)
		}
		var ids []string
		for id, item := range catalog {
			if (inventory && draft.InventoryIDs[id]) || (!inventory && lotCampaignMatches(draft.Search, id, item.Game, item.Name)) {
				ids = append(ids, id)
			}
		}
		slices.SortFunc(ids, func(a, b string) int {
			left, right := catalog[a], catalog[b]
			return strings.Compare(left.Game+" "+left.Name+" "+a, right.Game+" "+right.Name+" "+b)
		})
		var items []bot.SettingsButton
		for _, id := range ids {
			item := catalog[id]
			label := brief(item.Game, 22) + " · " + brief(item.Name, 37)
			items = append(items, bot.SettingsButton{Label: label, Action: link("pick", gameRef(id), "0")})
		}
		if inventory && draft.InventoryPartial {
			panel.Text += "\nTwitch вернул только часть данных о кампаниях и ранее полученных наградах."
		}
		if inventory && draft.InventoryIDs != nil && len(items) == 0 {
			panel.Text += "\nВ доступных данных этого аккаунта кампаний для выбора нет."
		} else if !inventory && len(items) == 0 {
			panel.Text += "\nСовпадений нет. Попробуйте другое название."
		} else if len(items) > 0 && len(draft.Selected) > 0 {
			panel.Text += "\nВыбор кампании другой игры сбросит черновик выбора."
		}
		bundlePageRows(&panel, arg(2), items, func(page int) string { return link(view, strconv.Itoa(page)) })
		if inventory {
			bundleButton(&panel, "🔄 Проверить ещё раз", scanInventory)
			bundleButton(&panel, "🔎 Найти кампанию", findCampaign)
		} else {
			bundleButton(&panel, "🔎 Новый поиск", findCampaign)
			bundleButton(&panel, "🎒 Из инвентаря лота", scanInventory)
			bundleButton(&panel, "⚙️ Дополнительно", link("tools"))
		}
		if draft.Game == "" {
			bundleButton(&panel, "⬅️ К выбору игры", link("games", "0"))
		} else {
			bundleButton(&panel, "⬅️ К кампаниям", link("campaigns", "0"))
		}
		return panel
	}
	if view == "pick" {
		for id, item := range catalog {
			if gameRef(id) == arg(2) {
				game := strings.ToLower(item.Game)
				if draft.Game != game {
					draft.Game, draft.Selected = game, make(map[string]map[string]bool)
					panel.Text = draftHeader()
				}
				view = "rewards"
				break
			}
		}
	}
	if view == "game" {
		for _, item := range catalog {
			game := strings.ToLower(item.Game)
			if gameRef(game) == arg(2) && draft.Game != game {
				draft.Game, draft.Selected = game, make(map[string]map[string]bool)
				panel.Text = draftHeader()
				break
			}
		}
		view = "campaigns"
	}
	if view == "cancel" {
		s.lotBundleDraft = nil
		panel.Text = "Редактирование закрыто."
		bundleButton(&panel, "⬅️ К списку лотов", "lb-list:0")
		return panel
	}
	if view == "games" {
		games := make(map[string]string)
		for _, item := range catalog {
			games[strings.ToLower(item.Game)] = item.Game
		}
		keys := make([]string, 0, len(games))
		for key := range games {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var items []bot.SettingsButton
		for _, key := range keys {
			items = append(items, bot.SettingsButton{Label: "🎮 " + brief(games[key], 60), Action: link("game", gameRef(key))})
		}
		panel.Text += "Выберите игру."
		if draft.Game != "" {
			panel.Text += " При выборе другой игры черновик выбора будет сброшен."
		}
		if len(keys) == 0 {
			panel.Text += " Каталог пока пуст."
		}
		bundlePageRows(&panel, arg(2), items, func(page int) string { return link("games", strconv.Itoa(page)) })
		bundleButton(&panel, "🎒 Из инвентаря лота", scanInventory)
		bundleButton(&panel, "🔎 Найти кампанию", findCampaign)
		bundleButton(&panel, "⚙️ Дополнительно", link("tools"))
		if draft.Game != "" {
			bundleButton(&panel, "⬅️ К кампаниям", link("campaigns", "0"))
		}
		bundleButton(&panel, "Отмена", link("cancel"))
		return panel
	}
	var ids []string
	for id, item := range catalog {
		if strings.EqualFold(item.Game, draft.Game) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		return strings.Compare(catalog[a].Name+a, catalog[b].Name+b)
	})
	if view == "rewards" || view == "t" || view == "all" || view == "none" {
		for _, id := range ids {
			if gameRef(id) != arg(2) {
				continue
			}
			item := catalog[id]
			selected := draft.Selected[id]
			switch view {
			case "all":
				selected = make(map[string]bool)
				for _, r := range item.Rewards {
					selected[r.ID] = true
				}
			case "none":
				selected = nil
			case "t":
				for _, r := range item.Rewards {
					if gameRef(r.ID) != arg(4) {
						continue
					}
					if selected == nil {
						selected = make(map[string]bool)
					}
					if selected[r.ID] {
						delete(selected, r.ID)
					} else {
						selected[r.ID] = true
					}
					break
				}
			}
			if len(selected) == 0 {
				delete(draft.Selected, id)
			} else {
				draft.Selected[id] = selected
			}
			panel.Text = draftHeader()
			panel.Text += fmt.Sprintf("🎯 %s\nОтмечено наград: %d из %d", item.Name, len(selected), len(item.Rewards))
			if _, inCatalog := s.state.LotCatalog[id]; !inCatalog && draft.Discovered[id].Game == "" {
				panel.Text += "\nКампания открыта из сохранённого состава лота."
			}
			if item.InventoryOnly {
				panel.Text += "\nКампания найдена по инвентарю. Сверьте список наград с составом лота."
			}
			var items []bot.SettingsButton
			for i, r := range item.Rewards {
				mark := "○ "
				if selected[r.ID] {
					mark = "✓ "
				}
				items = append(items, bot.SettingsButton{Label: fmt.Sprintf("%s%d. %s", mark, i+1, brief(r.Name, 50)), Action: link("t", arg(2), arg(3), gameRef(r.ID))})
			}
			bundlePageRows(&panel, arg(3), items, func(page int) string { return link("rewards", arg(2), strconv.Itoa(page)) })
			if len(selected) < len(item.Rewards) {
				bundleButton(&panel, "✅ Выбрать все награды", link("all", arg(2), arg(3)))
			}
			if len(selected) > 0 {
				bundleButton(&panel, "✖️ Снять выбор кампании", link("none", arg(2), arg(3)))
			}
			if len(draft.Selected) > 0 {
				bundleButton(&panel, "🔎 Просмотреть и сохранить", link("review"))
			}
			if draft.InventoryIDs[id] {
				bundleButton(&panel, "⬅️ К результатам инвентаря", link("inventory", "0"))
			}
			bundleButton(&panel, "⬅️ К кампаниям", link("campaigns", "0"))
			return panel
		}
		view = "campaigns"
	}
	if view == "review" || (view == "save" && kind == "lb-confirm") {
		var summary, lines []string
		count := 0
		for _, id := range ids {
			picked := draft.Selected[id]
			if len(picked) == 0 {
				continue
			}
			item := catalog[id]
			fields := []string{id}
			var names []string
			for i, r := range item.Rewards {
				if picked[r.ID] {
					fields = append(fields, r.ID)
					names = append(names, fmt.Sprintf("%d. %s", i+1, brief(r.Name, 30)))
				}
			}
			if len(names) != len(picked) {
				continue
			}
			lines = append(lines, strings.Join(fields, " "))
			count += len(names)
			detail := "все награды"
			if len(names) != len(item.Rewards) {
				detail = strings.Join(names[:min(3, len(names))], ", ")
				if len(names) > 3 {
					detail += fmt.Sprintf(" и ещё %d", len(names)-3)
				}
			}
			summary = append(summary, fmt.Sprintf("• %s · %d (%s)", brief(item.Name, 55), len(names), detail))
		}
		if len(lines) != len(draft.Selected) {
			panel.Text += "Состав каталога изменился. Проверьте отмеченные кампании."
		} else if len(lines) == 0 || len(lines) > 20 || count > 100 {
			panel.Text += "Выберите от 1 до 20 кампаний и не больше 100 наград."
		} else if view == "save" {
			if err := s.saveLotBundleFromCatalogLocked(draft.Ref, lines, catalog, draft.Discovered); err != nil {
				panel.Text += "⚠️ " + err.Error()
			} else {
				s.lotBundleDraft = nil
				panel.Text = fmt.Sprintf("%s\n\n✅ Состав сохранён. Кампаний · %d, наград · %d.\n\nВернитесь в Cardinal, чтобы продолжить настройку лота.", lotTitle, len(lines), count)
				bundleButton(&panel, "⬅️ К списку лотов", "lb-list:0")
				return panel
			}
		} else {
			panel.Title = "🔎 Предпросмотр состава лота"
			panel.Text += fmt.Sprintf("Кампаний: %d · наград: %d\n\n%s", len(lines), count, brief(strings.Join(summary, "\n"), 2800))
			bundleButton(&panel, "✅ Сохранить состав", "lb-confirm:"+draft.Token+":save")
		}
		bundleButton(&panel, "⬅️ К кампаниям", link("campaigns", "0"))
		return panel
	}
	var items []bot.SettingsButton
	for _, id := range ids {
		item := catalog[id]
		mark := "○ "
		if len(draft.Selected[id]) > 0 {
			mark = "✓ "
		}
		label := fmt.Sprintf("%s%s · %d/%d", mark, brief(item.Name, 49), len(draft.Selected[id]), len(item.Rewards))
		if _, inCatalog := s.state.LotCatalog[id]; !inCatalog && draft.Discovered[id].Game == "" {
			label += " · из состава лота"
		}
		items = append(items, bot.SettingsButton{Label: label, Action: link("rewards", gameRef(id), "0")})
	}
	panel.Text += "Откройте кампании и отметьте нужные награды. Можно выбрать несколько кампаний."
	if len(ids) == 0 {
		panel.Text += " В каталоге этой игры пока нет кампаний."
	}
	bundlePageRows(&panel, arg(2), items, func(page int) string { return link("campaigns", strconv.Itoa(page)) })
	if len(draft.Selected) > 0 {
		bundleButton(&panel, "🔎 Просмотреть и сохранить", link("review"))
	} else if len(s.state.ManagedLots[draft.Ref].Rewards) > 0 {
		panel.Text += "\n\nВ черновике нет отмеченных наград. Сохранённый состав действует до сохранения нового."
	}
	bundleButton(&panel, "🎮 Выбрать игру", link("games", "0"))
	bundleButton(&panel, "🎒 Из инвентаря лота", scanInventory)
	bundleButton(&panel, "🔎 Найти кампанию", findCampaign)
	bundleButton(&panel, "Отмена", link("cancel"))
	return panel
}
