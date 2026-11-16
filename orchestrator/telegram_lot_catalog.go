package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

const lotCatalogPage = "lot-catalog-page:"

type lotCatalogRemoval struct {
	ref, token string
	campaigns  map[string]lotCatalogCampaign
	lots       []string
	expires    time.Time
}

func lotCatalogGameRef(name string) string {
	return "g" + gameRef(strings.ToLower(name))
}

func (s *scheduler) telegramLotCatalog(value string) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lotCatalogPanel(value)
}

// Catalog selection and rendering run under the scheduler lock.
func (s *scheduler) lotCatalogSelection(ref string) (string, map[string]lotCatalogCampaign) {
	var title string
	campaigns := make(map[string]lotCatalogCampaign)
	for id, campaign := range s.state.LotCatalog {
		if ref == "c"+gameRef(id) {
			return campaign.Game + " · " + campaign.Name, map[string]lotCatalogCampaign{id: campaign}
		}
		if ref == lotCatalogGameRef(campaign.Game) {
			campaigns[id] = campaign
			if title == "" || campaign.Game < title {
				title = campaign.Game
			}
		}
	}
	return title, campaigns
}

func (s *scheduler) lotCatalogPanel(value string) bot.SettingsPanel {
	panel := bot.SettingsPanel{Title: "📚 Каталог кампаний"}
	ref, page, _ := strings.Cut(value, ":")
	if _, err := strconv.Atoi(ref); err == nil {
		games := make(map[string]string)
		for _, campaign := range s.state.LotCatalog {
			key := strings.ToLower(campaign.Game)
			if games[key] == "" || campaign.Game < games[key] {
				games[key] = campaign.Game
			}
		}
		keys := make([]string, 0, len(games))
		for key := range games {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var buttons []bot.SettingsButton
		for _, key := range keys {
			name := games[key]
			buttons = append(buttons, bot.SettingsButton{Label: "🎮 " + brief(name, 56), Action: lotCatalogPage + lotCatalogGameRef(name)})
		}
		panel.Text = "Выберите игру, чтобы открыть её кампании."
		if len(games) == 0 {
			panel.Text = "Каталог пока пуст."
		}
		bundlePageRows(&panel, ref, buttons, func(p int) string { return lotCatalogPage + strconv.Itoa(p) })
		bundleButton(&panel, "🔎 Найти кампанию", "prompt:lot-discover")
		bundleButton(&panel, "🏷 К лотам", "managed-lots")
		return panel
	}

	title, campaigns := s.lotCatalogSelection(ref)
	switch {
	case len(campaigns) == 0:
		panel.Text = "Запись уже отсутствует в каталоге."
	case strings.HasPrefix(ref, "g"):
		panel.Title = "🎮 " + brief(title, 72)
		panel.Text = fmt.Sprintf("Кампаний в каталоге · %d\nВыберите кампанию, чтобы увидеть её награды.", len(campaigns))
		ids := make([]string, 0, len(campaigns))
		for id := range campaigns {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b string) int {
			if order := strings.Compare(strings.ToLower(campaigns[a].Name), strings.ToLower(campaigns[b].Name)); order != 0 {
				return order
			}
			return strings.Compare(a, b)
		})
		var buttons []bot.SettingsButton
		for _, id := range ids {
			buttons = append(buttons, bot.SettingsButton{Label: "🏆 " + brief(campaigns[id].Name, 56), Action: lotCatalogPage + "c" + gameRef(id)})
		}
		bundlePageRows(&panel, page, buttons, func(p int) string { return fmt.Sprintf("%s%s:%d", lotCatalogPage, ref, p) })
		bundleButton(&panel, "🗑 Удалить игру из каталога", "lot-catalog-remove:"+ref)
	default:
		for id, campaign := range campaigns {
			var body strings.Builder
			fmt.Fprintf(&body, "%s\nID кампании — %s\n\n", title, id)
			if campaign.InventoryOnly {
				body.WriteString("Из инвентаря аккаунта. Сверьте количество наград с составом лота.\n\n")
			}
			for _, reward := range campaign.Rewards {
				fmt.Fprintf(&body, "• %s — %s\n", brief(reward.Name, 50), reward.ID)
			}
			if campaign.VerifiedExpires != nil {
				fmt.Fprintf(&body, "\nПодтверждённый срок · %s", campaign.VerifiedExpires.Format(time.RFC3339))
			}
			panel.Text = body.String()
			bundleButton(&panel, "🗑 Удалить кампанию из каталога", "lot-catalog-remove:"+ref)
			bundleButton(&panel, "⬅️ К кампаниям игры", lotCatalogPage+lotCatalogGameRef(campaign.Game))
		}
	}
	bundleButton(&panel, "📚 К каталогу", lotCatalogPage+"0")
	return panel
}

func (s *scheduler) telegramLotCatalogRemoval(kind, value string, now time.Time) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()

	panel := bot.SettingsPanel{Title: "🗑 Удаление из каталога"}
	ref := value
	var removal *lotCatalogRemoval
	if kind != "lot-catalog-remove" {
		removal = s.lotCatalogDelete
		if removal == nil || removal.token != value || !now.Before(removal.expires) {
			panel.Text = "Подтверждение устарело. Откройте каталог и запустите удаление заново."
			bundleButton(&panel, "📚 К каталогу", lotCatalogPage+"0")
			return panel
		}
		s.lotCatalogDelete = nil
		ref = removal.ref
		if kind == "lot-catalog-remove-cancel" {
			return s.lotCatalogPanel(ref)
		}
	}

	title, campaigns := s.lotCatalogSelection(ref)
	if len(campaigns) == 0 {
		return s.lotCatalogPanel(ref)
	}
	if removal != nil && !reflect.DeepEqual(removal.campaigns, campaigns) {
		panel.Text = "Состав каталога изменился. Проверьте список кампаний и повторите удаление."
		bundleButton(&panel, "⬅️ Назад", lotCatalogPage+ref)
		return panel
	}
	lots, used := s.lotCatalogUsedBy(campaigns)
	if removal != nil && !slices.Equal(lots, removal.lots) {
		panel.Text = "Связанные лоты изменились. Повторите удаление, чтобы увидеть актуальный список."
		bundleButton(&panel, "⬅️ Назад", lotCatalogPage+ref)
		return panel
	}

	isGame := strings.HasPrefix(ref, "g")
	if removal == nil {
		var key [12]byte
		if _, err := rand.Read(key[:]); err != nil {
			panel.Text = "Не удалось открыть подтверждение. Попробуйте ещё раз."
			bundleButton(&panel, "⬅️ Назад", lotCatalogPage+ref)
			return panel
		}
		token := hex.EncodeToString(key[:])
		s.lotCatalogDelete = &lotCatalogRemoval{ref: ref, token: token, campaigns: campaigns, lots: lots, expires: now.Add(2 * time.Minute)}
		panel.Text = "Удалить кампанию из каталога?\n\n" + title
		if isGame {
			panel.Text = fmt.Sprintf("Удалить игру из каталога?\n\n%s\nКампаний к удалению · %d", title, len(campaigns))
		}
		if len(lots) > 0 {
			panel.Text += "\n\nСохранённый состав следующих лотов продолжит действовать на FunPay."
			for _, lotRef := range lots[:min(8, len(lots))] {
				name := s.state.LotTitles[lotRef]
				if name == "" {
					name = lotRef
				}
				panel.Text += "\n• " + brief(name, 92)
			}
			if len(lots) > 8 {
				panel.Text += fmt.Sprintf("\n… и ещё %d", len(lots)-8)
			}
		}
		bundleButton(&panel, "⚠️ Подтвердить удаление", "lot-catalog-remove-confirm:"+token)
		bundleButton(&panel, "↩️ Отмена", "lot-catalog-remove-cancel:"+token)
		return panel
	}

	previous := s.state.LotCatalog
	previousArchive := s.state.LotArchivedCampaigns
	s.state.LotCatalog = maps.Clone(previous)
	if len(used) > 0 {
		s.state.LotArchivedCampaigns = maps.Clone(previousArchive)
		if s.state.LotArchivedCampaigns == nil {
			s.state.LotArchivedCampaigns = make(map[string]lotCatalogCampaign)
		}
		for id := range used {
			s.state.LotArchivedCampaigns[id] = campaigns[id]
		}
	}
	for id := range campaigns {
		delete(s.state.LotCatalog, id)
	}
	if !s.persist() {
		s.state.LotCatalog = previous
		s.state.LotArchivedCampaigns = previousArchive
		panel.Text = "Не удалось сохранить изменение каталога. Попробуйте ещё раз."
		bundleButton(&panel, "⬅️ Назад", lotCatalogPage+ref)
		return panel
	}

	back := "0"
	if draft := s.lotBundleDraft; draft != nil {
		catalog := s.lotCatalogFor(draft.Ref)
		for id := range campaigns {
			if _, available := catalog[id]; !available {
				delete(draft.Selected, id)
			}
		}
	}
	for _, campaign := range campaigns {
		if !isGame {
			gameRef := lotCatalogGameRef(campaign.Game)
			if _, remaining := s.lotCatalogSelection(gameRef); len(remaining) > 0 {
				back = gameRef
			}
		}
	}
	panel = s.lotCatalogPanel(back)
	result := "✅ Кампания удалена из каталога."
	if isGame {
		result = "✅ Игра удалена из каталога."
	}
	panel.Text = result + "\n\n" + panel.Text
	return panel
}

func (s *scheduler) lotCatalogUsedBy(campaigns map[string]lotCatalogCampaign) ([]string, map[string]bool) {
	var refs []string
	used := make(map[string]bool)
	for ref, lot := range s.state.ManagedLots {
		linked := false
		for _, reward := range lot.Rewards {
			if _, selected := campaigns[reward.Campaign]; selected {
				used[reward.Campaign] = true
				linked = true
			}
		}
		if linked {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	return refs, used
}
