package orchestrator

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type lotCatalogCampaign struct {
	Game, Name      string
	Rewards         []lotReward
	Benefits        map[string][]string `json:",omitempty"`
	VerifiedExpires *time.Time          `json:",omitempty"`
	Source          string              `json:",omitempty"`
	InventoryOnly   bool                `json:",omitempty"`
}

func (s *scheduler) telegramManagedLots() bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := fmt.Sprintf("📚 Каталог кампаний: %d\n🔗 Настроено лотов: %d\n\nЧтобы включить автоматическую проверку, откройте выбранный лот в боте Cardinal.", len(s.state.LotCatalog), len(s.state.ManagedLots))
	return bot.SettingsPanel{Title: "🏷 Лоты", Text: text, Buttons: [][]bot.SettingsButton{
		{{Label: "📚 Каталог", Action: "lot-catalog-page:0"}, {Label: "➕ Добавить кампанию", Action: "prompt:lot-catalog"}},
		{{Label: "🔎 Загрузить кампании из Twitch", Action: "prompt:lot-discover"}},
		{{Label: "🎒 Найти в инвентаре аккаунта", Action: "prompt:lot-inventory"}},
		{{Label: "📋 Состав лота", Action: "lb-list:0"}},
		{{Label: "📎 Загрузить аккаунты лотов", Action: "prompt:upload:lot-credentials"}},
		{{Label: "🔎 Ручной предпросмотр", Action: "lot-preview"}},
		{{Label: "🏠 В меню", Action: "menu"}},
	}}
}

func (s *scheduler) telegramLotCatalog(value string) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "📚 Каталог лотов"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	ids := make([]string, 0, len(s.state.LotCatalog))
	for id := range s.state.LotCatalog {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		left, right := s.state.LotCatalog[a], s.state.LotCatalog[b]
		return strings.Compare(left.Game+" "+left.Name+" "+a, right.Game+" "+right.Name+" "+b)
	})
	if page, err := strconv.Atoi(value); err == nil {
		page = min(max(0, page), max(0, len(ids)-1)/8)
		end := min(len(ids), (page+1)*8)
		for _, id := range ids[page*8 : end] {
			item := s.state.LotCatalog[id]
			row(brief(item.Game+" · "+item.Name, 52), "lot-catalog-page:c"+gameRef(id))
		}
		panel.Text = "Выберите кампанию, чтобы увидеть ID и состав наград."
		if len(ids) == 0 {
			panel.Text = "Каталог пока пуст."
		}
		if page > 0 {
			row("⬅️ Назад", fmt.Sprintf("lot-catalog-page:%d", page-1))
		}
		if end < len(ids) {
			row("➡️ Дальше", fmt.Sprintf("lot-catalog-page:%d", page+1))
		}
	} else {
		for _, id := range ids {
			if value != "c"+gameRef(id) {
				continue
			}
			item := s.state.LotCatalog[id]
			var text strings.Builder
			fmt.Fprintf(&text, "%s · %s\nID кампании — %s\n\n", item.Game, item.Name, id)
			if item.InventoryOnly {
				text.WriteString("Из инвентаря аккаунта. Сверьте количество наград с составом лота.\n\n")
			}
			for _, reward := range item.Rewards {
				fmt.Fprintf(&text, "• %s — %s\n", brief(reward.Name, 50), reward.ID)
			}
			if item.VerifiedExpires != nil {
				fmt.Fprintf(&text, "\nПодтверждённый срок · %s", item.VerifiedExpires.Format(time.RFC3339))
			}
			panel.Text = text.String()
			break
		}
		row("⬅️ К каталогу", "lot-catalog-page:0")
	}
	row("➕ Добавить кампанию", "prompt:lot-catalog")
	row("🏷 К лотам", "managed-lots")
	return panel
}

func (s *scheduler) saveManagedLotInput(kind, text string) error {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "lot-catalog" {
		if len(lines) < 3 || len(lines) > 33 || !validLotLabel(strings.TrimSpace(lines[0])) {
			return fmt.Errorf("укажите игру, кампанию и от 1 до 30 наград")
		}
		id, name, ok := lotCatalogLine(lines[1])
		if !ok {
			return fmt.Errorf("проверьте ID и название кампании")
		}
		item := lotCatalogCampaign{Game: strings.TrimSpace(lines[0]), Name: name, Benefits: make(map[string][]string)}
		seen := make(map[string]bool)
		for _, line := range lines[2:] {
			fields := strings.Split(line, "|")
			if len(fields) == 3 && strings.EqualFold(strings.TrimSpace(fields[0]), "срок") {
				expiry, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[1]))
				source := strings.TrimSpace(fields[2])
				sourceURL, urlErr := url.Parse(source)
				if item.VerifiedExpires != nil || err != nil || urlErr != nil || sourceURL.Scheme != "https" || sourceURL.Hostname() == "" || sourceURL.User != nil || len(source) > 400 {
					return fmt.Errorf("укажите один подтверждённый срок в формате RFC3339 и HTTPS-ссылку на правило")
				}
				item.VerifiedExpires, item.Source = &expiry, source
				continue
			}
			if len(fields) != 2 && len(fields) != 3 {
				return fmt.Errorf("проверьте формат строк наград")
			}
			reward, title, ok := lotCatalogLine(strings.Join(fields[:2], "|"))
			if !ok || seen[reward] {
				return fmt.Errorf("укажите разные ID и названия наград")
			}
			seen[reward] = true
			item.Rewards = append(item.Rewards, lotReward{Campaign: id, ID: reward, Name: title})
			if len(fields) == 3 {
				item.Benefits[reward] = strings.Fields(fields[2])
				for _, benefit := range item.Benefits[reward] {
					if !validLotID(benefit) {
						return fmt.Errorf("проверьте ID предмета Twitch у награды %s", reward)
					}
				}
			}
		}
		if len(item.Rewards) == 0 || len(item.Rewards) > 30 {
			return fmt.Errorf("нужно от 1 до 30 наград")
		}
		if s.state.LotCatalog == nil {
			s.state.LotCatalog = make(map[string]lotCatalogCampaign)
		}
		prior, exists := s.state.LotCatalog[id]
		s.state.LotCatalog[id] = item
		if s.persist() {
			return nil
		}
		if exists {
			s.state.LotCatalog[id] = prior
		} else {
			delete(s.state.LotCatalog, id)
		}
		return fmt.Errorf("не удалось сохранить каталог")
	}
	ref := strings.ToLower(strings.TrimSpace(lines[0]))
	return s.saveLotBundleLocked(ref, lines[1:])
}

func (s *scheduler) saveLotBundleLocked(ref string, campaigns []string) error {
	if len(campaigns) < 1 || len(campaigns) > 20 || !validLotRef(ref) {
		return fmt.Errorf("укажите код лота и от 1 до 20 кампаний")
	}
	lot := managedLot{Benefits: make(map[string][]string)}
	allDeadlines := true
	seen := make(map[string]bool)
	for _, line := range campaigns {
		fields := strings.Fields(line)
		if len(fields) == 0 || seen[fields[0]] {
			return fmt.Errorf("укажите каждую кампанию один раз")
		}
		id := fields[0]
		item, ok := s.state.LotCatalog[id]
		if !ok {
			return fmt.Errorf("кампания %s отсутствует в каталоге лотов", id)
		}
		if lot.Game != "" && !strings.EqualFold(lot.Game, item.Game) {
			return fmt.Errorf("выберите кампании одной игры")
		}
		lot.Game = item.Game
		seen[id] = true
		if item.VerifiedExpires == nil {
			allDeadlines = false
		} else {
			if lot.SuggestedCutoff == nil || item.VerifiedExpires.Before(*lot.SuggestedCutoff) {
				lot.SuggestedCutoff = item.VerifiedExpires
			}
			if !slices.Contains(lot.DeadlineSources, item.Source) {
				lot.DeadlineSources = append(lot.DeadlineSources, item.Source)
			}
		}
		for reward, benefits := range item.Benefits {
			lot.Benefits[id+"/"+reward] = benefits
		}
		if len(fields) == 1 {
			lot.Rewards = append(lot.Rewards, item.Rewards...)
			continue
		}
		for _, reward := range fields[1:] {
			index := slices.IndexFunc(item.Rewards, func(r lotReward) bool { return r.ID == reward })
			if index < 0 {
				return fmt.Errorf("награды %s нет в кампании %s", reward, id)
			}
			if !slices.Contains(lot.Rewards, item.Rewards[index]) {
				lot.Rewards = append(lot.Rewards, item.Rewards[index])
			}
		}
	}
	if len(lot.Rewards) == 0 || len(lot.Rewards) > 100 {
		return fmt.Errorf("в лоте должно быть от 1 до 100 наград")
	}
	if !allDeadlines {
		lot.SuggestedCutoff = nil
		lot.DeadlineSources = nil
	}
	for key, game := range s.state.Games {
		if strings.EqualFold(game.Name, lot.Game) && s.claimJobRunning(key) {
			return fmt.Errorf("дождитесь завершения получения Drops для этой игры")
		}
	}
	previousPolicies := s.activeLotClaimPolicies()
	if s.state.ManagedLots == nil {
		s.state.ManagedLots = make(map[string]managedLot)
	}
	prior, exists := s.state.ManagedLots[ref]
	s.state.ManagedLots[ref] = lot
	if s.persist() {
		s.refreshLotClaimPolicies(previousPolicies)
		return nil
	}
	if exists {
		s.state.ManagedLots[ref] = prior
	} else {
		delete(s.state.ManagedLots, ref)
	}
	return fmt.Errorf("не удалось сохранить состав")
}

func lotCatalogLine(line string) (string, string, bool) {
	id, name, ok := strings.Cut(line, "|")
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	return id, name, ok && validLotID(id) && validLotLabel(name)
}

func validLotLabel(value string) bool { return value != "" && len([]rune(value)) <= 100 }

func validLotID(id string) bool {
	return id != "" && len(id) <= 100 && !strings.ContainsAny(id, " \t\r\n/|")
}

func (s *scheduler) discoverLotCampaigns(query, login string) (int, error) {
	query = strings.TrimSpace(query)
	if !validLotLabel(query) {
		return 0, fmt.Errorf("укажите название игры или кампании")
	}
	login = strings.ToLower(strings.TrimSpace(login))
	s.mu.Lock()
	var accounts []parser.Account
	var history []twitch.LotCampaign
	if login != "" {
		if a, ok := s.state.LotCredentials[login]; ok {
			accounts = append(accounts, a)
		}
		if a, ok := s.state.LotAccounts[login]; ok {
			accounts = append(accounts, a.Credentials)
		}
	} else {
		for _, game := range s.state.Games {
			for _, record := range game.Campaigns {
				if record == nil || len(record.Rewards) == 0 {
					continue
				}
				item := twitch.LotCampaign{ID: record.ID, Name: record.Name}
				item.Game.Name = game.Name
				for _, reward := range record.Rewards {
					item.TimeBasedDrops = append(item.TimeBasedDrops, twitch.LotCampaignDrop{ID: reward.ID, Name: reward.Name})
				}
				history = append(history, item)
			}
		}
		for _, a := range s.state.LotCredentials {
			accounts = append(accounts, a)
			if len(accounts) == 3 {
				break
			}
		}
	}
	for _, a := range s.state.Accounts {
		if len(accounts) == 3 || (login != "" && len(accounts) > 0) {
			break
		}
		if !a.Quarantined && (login == "" || strings.EqualFold(a.Credentials.Login, login)) {
			accounts = append(accounts, a.Credentials)
		}
	}
	cfg := s.miner
	s.mu.Unlock()
	if len(accounts) == 0 && login != "" {
		return 0, fmt.Errorf("аккаунт не найден. Загрузите его полный TXT в «Лоты → Загрузить аккаунты лотов»")
	}
	if len(accounts) == 0 && len(history) == 0 {
		return 0, fmt.Errorf("для каталога нужен хотя бы один аккаунт Twitch")
	}
	cfg.Diagnostics = false
	client := &http.Client{Timeout: 8 * time.Second}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var remote []twitch.LotCampaign
	var err error
	for _, account := range accounts {
		gql := twitch.NewGQLClient(client, &cfg, &account)
		if login == "" {
			remote, err = gql.LotCampaigns(ctx)
		} else {
			remote, err = gql.LotCampaignsInInventory(ctx)
		}
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil && login != "" {
		return 0, fmt.Errorf("Twitch не подтвердил инвентарь аккаунта. Проверьте его авторизацию и повторите поиск")
	}
	added, saveErr := s.importLotCampaigns(query, append(remote, history...), login != "")
	if err != nil && saveErr == nil && added == 0 {
		return 0, fmt.Errorf("Twitch не подтвердил каталог. Проверьте сохранённые кампании или поищите в инвентаре аккаунта")
	}
	return added, saveErr
}

func (s *scheduler) importLotCampaigns(query string, campaigns []twitch.LotCampaign, inventoryOnly bool) (int, error) {
	query = strings.ToLower(query)
	exactGame := slices.ContainsFunc(campaigns, func(c twitch.LotCampaign) bool { return strings.EqualFold(c.Game.Name, query) })
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.LotCatalog
	catalog := maps.Clone(previous)
	if catalog == nil {
		catalog = make(map[string]lotCatalogCampaign)
	}
	updated := make(map[string]bool)
	for _, campaign := range campaigns {
		name := strings.ToLower(campaign.Game.Name)
		matches := strings.Contains(name, query) || strings.Contains(strings.ToLower(campaign.Name), query)
		if name == "" || campaign.Name == "" || (exactGame && name != query) || !matches || !validLotID(campaign.ID) || len(campaign.TimeBasedDrops) == 0 {
			continue
		}
		existing, exists := catalog[campaign.ID]
		if exists && (!existing.InventoryOnly || !strings.EqualFold(existing.Game, campaign.Game.Name)) {
			continue
		}
		item := lotCatalogCampaign{Game: campaign.Game.Name, Name: campaign.Name, Benefits: make(map[string][]string), InventoryOnly: inventoryOnly}
		seen := make(map[string]bool)
		for _, drop := range campaign.TimeBasedDrops {
			if !validLotID(drop.ID) || drop.Name == "" || seen[drop.ID] {
				continue
			}
			seen[drop.ID] = true
			item.Rewards = append(item.Rewards, lotReward{Campaign: campaign.ID, ID: drop.ID, Name: drop.Name})
			for _, edge := range drop.BenefitEdges {
				if validLotID(edge.Benefit.ID) {
					item.Benefits[drop.ID] = append(item.Benefits[drop.ID], edge.Benefit.ID)
				}
			}
		}
		if len(item.Rewards) == 0 {
			continue
		}
		if exists && inventoryOnly {
			merged := existing
			merged.Rewards = slices.Clone(existing.Rewards)
			merged.Benefits = maps.Clone(existing.Benefits)
			if merged.Benefits == nil {
				merged.Benefits = make(map[string][]string)
			}
			for _, reward := range item.Rewards {
				if !slices.ContainsFunc(merged.Rewards, func(r lotReward) bool { return r.ID == reward.ID }) {
					merged.Rewards = append(merged.Rewards, reward)
				}
				if len(merged.Benefits[reward.ID]) == 0 && len(item.Benefits[reward.ID]) > 0 {
					merged.Benefits[reward.ID] = item.Benefits[reward.ID]
				}
			}
			if len(merged.Rewards) == len(existing.Rewards) && len(merged.Benefits) == len(existing.Benefits) {
				continue
			}
			item = merged
		}
		catalog[campaign.ID] = item
		updated[campaign.ID] = true
	}
	if len(updated) == 0 {
		return 0, nil
	}
	s.state.LotCatalog = catalog
	if s.persist() {
		return len(updated), nil
	}
	s.state.LotCatalog = previous
	return 0, fmt.Errorf("не удалось сохранить каталог")
}
