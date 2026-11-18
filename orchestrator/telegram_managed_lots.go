package orchestrator

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
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
	text := fmt.Sprintf("📋 Сохранённых составов · %d\n📚 Кампаний в каталоге · %d", len(s.state.ManagedLots), len(s.state.LotCatalog))
	return bot.SettingsPanel{Title: "🏷 Лоты", Text: text, Buttons: [][]bot.SettingsButton{
		{{Label: "📋 Составы лотов", Action: "lb-list:0"}},
		{{Label: "📚 Каталог кампаний", Action: "lot-catalog-page:0"}},
		{{Label: "⚙️ Дополнительно", Action: "lot-tools"}},
		{{Label: "🏠 В меню", Action: "menu"}},
	}}
}

func telegramLotTools() bot.SettingsPanel {
	return bot.SettingsPanel{Title: "⚙️ Лоты · дополнительно", Buttons: [][]bot.SettingsButton{
		{{Label: "⌨️ Ввести код лота", Action: "prompt:lb-code"}},
		{{Label: "➕ Добавить кампанию по ID", Action: "prompt:lot-catalog"}},
		{{Label: "🎒 Искать в инвентаре по логину", Action: "prompt:lot-inventory"}},
		{{Label: "📎 Загрузить аккаунты лотов", Action: "prompt:upload:lot-credentials"}},
		{{Label: "🔎 Ручной предпросмотр по коду", Action: "lot-preview"}},
		{{Label: "⬅️ К лотам", Action: "managed-lots"}},
	}}
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
	return s.saveLotBundleFromCatalogLocked(ref, campaigns, s.lotCatalogFor(ref), nil)
}

func (s *scheduler) saveLotBundleFromCatalogLocked(ref string, campaigns []string, catalog, discovered map[string]lotCatalogCampaign) error {
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
		item, ok := catalog[id]
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
	previousCatalog := s.state.LotCatalog
	known := s.lotCatalogFor(ref)
	var updatedCatalog map[string]lotCatalogCampaign
	for id := range seen {
		item, found := discovered[id]
		if !found {
			continue
		}
		if previous, exists := known[id]; exists {
			var changed bool
			item, changed = mergeLotInventoryCampaign(previous, item)
			if !changed {
				continue
			}
		}
		if updatedCatalog == nil {
			updatedCatalog = make(map[string]lotCatalogCampaign, len(previousCatalog)+len(seen))
			maps.Copy(updatedCatalog, previousCatalog)
		}
		updatedCatalog[id] = item
	}
	if updatedCatalog != nil {
		s.state.LotCatalog = updatedCatalog
	}
	s.state.ManagedLots[ref] = lot
	if s.persist() {
		s.refreshLotClaimPolicies(previousPolicies)
		return nil
	}
	s.state.LotCatalog = previousCatalog
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

var lotCampaignUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func normalizeLotCampaignQuery(input string) (string, bool) {
	query := strings.TrimSpace(input)
	if strings.Contains(query, "://") {
		link, err := url.Parse(query)
		if err != nil || link.Scheme != "https" || link.User != nil ||
			(!strings.EqualFold(link.Host, "www.twitch.tv") && !strings.EqualFold(link.Host, "twitch.tv")) ||
			strings.TrimSuffix(link.Path, "/") != "/drops/campaigns" {
			return "", false
		}
		ids := link.Query()["dropID"]
		if len(ids) != 1 || !lotCampaignUUID.MatchString(ids[0]) {
			return "", false
		}
		query = ids[0]
	}
	if lotCampaignUUID.MatchString(query) {
		query = strings.ToLower(query)
	}
	return query, validLotLabel(query)
}

func lotCampaignMatches(query, id, game, name string) bool {
	if lotCampaignUUID.MatchString(query) {
		return strings.EqualFold(id, query)
	}
	query = strings.ToLower(query)
	return query != "" && (strings.Contains(strings.ToLower(game), query) || strings.Contains(strings.ToLower(name), query))
}

func (s *scheduler) discoverLotCampaigns(query, login string) (int, error) {
	query, valid := normalizeLotCampaignQuery(query)
	if !valid {
		return 0, fmt.Errorf("укажите название игры, кампании или ссылку Twitch Drops")
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
		return 0, fmt.Errorf("аккаунт не найден. Загрузите его полный TXT в «Лоты → Дополнительно → Загрузить аккаунты лотов»")
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
	answered := false
	for _, account := range accounts {
		gql := twitch.NewGQLClient(client, &cfg, &account)
		var found []twitch.LotCampaign
		var err error
		if login == "" {
			if lotCampaignUUID.MatchString(query) {
				campaign, lookupErr := gql.LotCampaignByID(ctx, query)
				answered = answered || lookupErr == nil
				if campaign != nil && len(campaign.TimeBasedDrops) > 0 {
					remote = []twitch.LotCampaign{*campaign}
					break
				}
			}
			found, err = gql.LotCampaigns(ctx)
		} else {
			found, err = gql.LotCampaignsInInventory(ctx)
		}
		if err == nil {
			answered = true
			if login != "" || slices.ContainsFunc(found, func(c twitch.LotCampaign) bool {
				return len(c.TimeBasedDrops) > 0 && lotCampaignMatches(query, c.ID, c.Game.Name, c.Name)
			}) {
				remote = found
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if !answered && login != "" {
		return 0, fmt.Errorf("Twitch не подтвердил инвентарь аккаунта. Проверьте его авторизацию и повторите поиск")
	}
	added, saveErr := s.importLotCampaigns(query, append(remote, history...), login != "")
	if !answered && len(accounts) > 0 && saveErr == nil && added == 0 {
		return 0, fmt.Errorf("Twitch не подтвердил каталог. Проверьте сохранённые кампании или поищите в инвентаре аккаунта")
	}
	return added, saveErr
}

func (s *scheduler) importLotCampaigns(query string, campaigns []twitch.LotCampaign, inventoryOnly bool) (int, error) {
	query = strings.ToLower(query)
	exactGame := !lotCampaignUUID.MatchString(query) && slices.ContainsFunc(campaigns, func(c twitch.LotCampaign) bool {
		return len(c.TimeBasedDrops) > 0 && strings.EqualFold(c.Game.Name, query)
	})
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
		matches := lotCampaignMatches(query, campaign.ID, campaign.Game.Name, campaign.Name)
		if name == "" || campaign.Name == "" || (exactGame && name != query) || !matches || !validLotID(campaign.ID) || len(campaign.TimeBasedDrops) == 0 {
			continue
		}
		existing, exists := catalog[campaign.ID]
		if exists && (!existing.InventoryOnly || !strings.EqualFold(existing.Game, campaign.Game.Name)) {
			continue
		}
		item, ok := lotCampaignFromTwitch(campaign, inventoryOnly)
		if !ok {
			continue
		}
		if exists && inventoryOnly {
			var changed bool
			item, changed = mergeLotInventoryCampaign(existing, item)
			if !changed {
				continue
			}
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

func mergeLotInventoryCampaign(existing, discovered lotCatalogCampaign) (lotCatalogCampaign, bool) {
	if !existing.InventoryOnly || !strings.EqualFold(existing.Game, discovered.Game) {
		return existing, false
	}
	merged := existing
	merged.Rewards = slices.Clone(existing.Rewards)
	merged.Benefits = make(map[string][]string, len(existing.Benefits))
	maps.Copy(merged.Benefits, existing.Benefits)
	changed := false
	for _, reward := range discovered.Rewards {
		if !slices.ContainsFunc(merged.Rewards, func(r lotReward) bool { return r.ID == reward.ID }) {
			merged.Rewards = append(merged.Rewards, reward)
			changed = true
		}
		if len(merged.Benefits[reward.ID]) == 0 && len(discovered.Benefits[reward.ID]) > 0 {
			merged.Benefits[reward.ID] = discovered.Benefits[reward.ID]
			changed = true
		}
	}
	return merged, changed
}

func lotCampaignFromTwitch(campaign twitch.LotCampaign, inventoryOnly bool) (lotCatalogCampaign, bool) {
	if campaign.Game.Name == "" || campaign.Name == "" || !validLotID(campaign.ID) {
		return lotCatalogCampaign{}, false
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
	return item, len(item.Rewards) > 0
}
