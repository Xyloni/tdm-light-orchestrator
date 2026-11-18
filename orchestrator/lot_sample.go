package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type lotSampleAccount struct {
	Login, OAuth, UserID, DeviceID string
}

type lotRegistrationSample struct {
	accounts []lotSampleAccount
	expires  time.Time
}

func validLotSample(sample []lotSampleAccount) bool {
	if len(sample) > 5 {
		return false
	}
	for _, account := range sample {
		if !validLotLogin(account.Login) {
			return false
		}
		if account.OAuth == "" && account.UserID == "" && account.DeviceID == "" {
			continue
		}
		if account.OAuth == "" || account.DeviceID == "" || len(account.OAuth) > 512 || len(account.DeviceID) > 128 ||
			account.UserID == "" || len(account.UserID) > 20 || strings.Trim(account.UserID, "0123456789") != "" {
			return false
		}
		for _, ch := range account.OAuth + account.DeviceID {
			if ch < '!' || ch > '~' {
				return false
			}
		}
	}
	return true
}

func (s *scheduler) storeLotSampleLocked(ref string, sample []lotSampleAccount) {
	if sample == nil {
		return
	}
	const ttl = 15 * time.Minute
	entry := lotRegistrationSample{accounts: sample, expires: time.Now().Add(ttl)}
	if s.lotSamples == nil {
		s.lotSamples = make(map[string]lotRegistrationSample)
	}
	s.lotSamples[ref] = entry
	time.AfterFunc(ttl, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.lotSamples[ref].expires == entry.expires {
			delete(s.lotSamples, ref)
		}
	})
}

func (s *scheduler) hasLotCatalogMatch(query, draftToken string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	catalog := s.state.LotCatalog
	if draft := s.lotBundleDraft; draft != nil && draft.Token == draftToken {
		catalog = s.lotBundleCatalog(draft)
	}
	for id, item := range catalog {
		if lotCampaignMatches(query, id, item.Game, item.Name) {
			return true
		}
	}
	return false
}

func (s *scheduler) credentialsForLotSample(ref string, sample []lotSampleAccount) []parser.Account {
	accounts := make([]parser.Account, 0, len(sample))
	seen := make(map[string]bool, len(sample))
	for _, candidate := range sample {
		login := strings.ToLower(candidate.Login)
		if seen[login] || s.state.LotSold[lotAccountReceipt("login", login)] {
			continue
		}
		seen[login] = true
		known := s.state.LotCredentials[login]
		if held, exists := s.state.LotAccounts[login]; exists {
			if held.Lot != ref {
				continue
			}
			known = held.Credentials
		}
		if known.OAuth == "" {
			for _, account := range s.state.Accounts {
				if !account.Quarantined && strings.EqualFold(account.Credentials.Login, login) {
					known = account.Credentials
					break
				}
			}
		}
		if candidate.OAuth != "" && (known.UserID == "" || candidate.UserID == known.UserID) {
			known = parser.Account{Login: login, OAuth: candidate.OAuth, UserID: candidate.UserID, DeviceID: candidate.DeviceID}
		}
		if known.OAuth != "" && known.UserID != "" && !s.state.LotSold[lotAccountReceipt("id", known.UserID)] {
			accounts = append(accounts, known)
		}
	}
	return accounts
}

func (s *scheduler) lotSampleAccounts(draftToken string) (string, []parser.Account, config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.miner
	cfg.Diagnostics = false
	draft := s.lotBundleDraft
	if draft == nil || draft.Token != draftToken {
		return "", nil, cfg, fmt.Errorf("откройте лот заново")
	}
	sample := s.lotSamples[draft.Ref]
	if !time.Now().Before(sample.expires) {
		delete(s.lotSamples, draft.Ref)
		return "", nil, cfg, fmt.Errorf("откройте лот в Cardinal и снова откройте состав в оркестраторе, чтобы обновить выборку из TXT")
	}
	if len(sample.accounts) == 0 {
		return "", nil, cfg, fmt.Errorf("в TXT этого лота нет подходящих строк с аккаунтами")
	}
	accounts := s.credentialsForLotSample(draft.Ref, sample.accounts)
	if len(accounts) == 0 {
		return "", nil, cfg, fmt.Errorf("для аккаунтов из TXT нужны данные Twitch. Загрузите полный TXT в «Лоты → Дополнительно → Загрузить аккаунты лотов» и повторите поиск")
	}
	return draft.Ref, accounts, cfg, nil
}

func (s *scheduler) discoverLotSampleCampaigns(query, draftToken string) (int, error) {
	query, valid := normalizeLotCampaignQuery(query)
	if !valid {
		return 0, fmt.Errorf("укажите название игры, кампании или ссылку Twitch Drops")
	}
	_, accounts, cfg, err := s.lotSampleAccounts(draftToken)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var campaigns []twitch.LotCampaign
	checked := 0
	for _, account := range accounts {
		found, err := twitch.NewGQLClient(client, &cfg, &account).LotCampaignsInInventory(ctx)
		if err == nil {
			campaigns = append(campaigns, found...)
			checked++
		}
		if ctx.Err() != nil {
			break
		}
	}
	if checked == 0 {
		return 0, fmt.Errorf("Twitch не подтвердил инвентарь аккаунтов из лота. Проверьте авторизацию и повторите поиск")
	}
	return s.importLotCampaigns(query, campaigns, true)
}

func (s *scheduler) scanLotInventory(draftToken string) (map[string]lotCatalogCampaign, bool, error) {
	ref, accounts, cfg, err := s.lotSampleAccounts(draftToken)
	if err != nil {
		return nil, false, err
	}
	client := &http.Client{Timeout: 4 * time.Second}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	for _, account := range accounts {
		gql := twitch.NewGQLClient(client, &cfg, &account)
		campaigns, err := gql.LotCampaignsInInventory(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		found := make(map[string]lotCatalogCampaign)
		partial := false
		for _, campaign := range campaigns {
			if item, ok := lotCampaignFromTwitch(campaign, true); ok {
				found[campaign.ID] = item
			} else {
				partial = true
			}
		}
		claimed, historyErr := gql.ClaimedLotCampaignIDs(ctx)
		partial = partial || historyErr != nil
		s.mu.Lock()
		known := s.lotCatalogFor(ref)
		for _, id := range claimed {
			if item, ok := known[id]; ok && found[id].Game == "" {
				found[id] = item
			}
		}
		s.mu.Unlock()
		for _, id := range claimed {
			if _, ok := found[id]; ok {
				continue
			}
			if validLotID(id) && ctx.Err() == nil {
				campaign, err := gql.LotCampaignByID(ctx, id)
				if err == nil && campaign != nil {
					if item, ok := lotCampaignFromTwitch(*campaign, true); ok {
						found[campaign.ID] = item
						continue
					}
				}
			}
			partial = true
		}
		return found, partial, nil
	}
	return nil, false, fmt.Errorf("Twitch не подтвердил инвентарь аккаунтов из лота. Проверьте авторизацию и повторите поиск")
}
