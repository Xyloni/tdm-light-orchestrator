package orchestrator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type managedLot struct {
	Game            string
	Rewards         []lotReward
	Benefits        map[string][]string `json:",omitempty"`
	SuggestedCutoff *time.Time          `json:",omitempty"`
	DeadlineSources []string            `json:",omitempty"`
}

type lotAccount struct {
	Lot         string
	Credentials parser.Account
}

type managedLotRequest struct {
	Operation, Lot, Login, AccountLine string
}

func (s *scheduler) uploadLotCredentials(data []byte) error {
	groups, err := parser.Parse(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.LotCredentials
	bank := maps.Clone(previous)
	if bank == nil {
		bank = make(map[string]parser.Account)
	}
	byID := make(map[string]string)
	for login, a := range bank {
		byID[a.UserID] = login
	}
	for _, a := range s.state.Accounts {
		byID[a.Credentials.UserID] = a.Credentials.Login
	}
	for _, a := range s.state.LotAccounts {
		byID[a.Credentials.UserID] = a.Credentials.Login
	}
	for _, group := range groups {
		for _, a := range group {
			if login := byID[a.UserID]; login != "" && login != a.Login {
				return fmt.Errorf("Twitch user ID аккаунта %s уже связан с другим логином", a.Login)
			}
			if s.state.LotSold[lotAccountReceipt("login", a.Login)] || s.state.LotSold[lotAccountReceipt("id", a.UserID)] {
				return fmt.Errorf("аккаунт %s уже продан", a.Login)
			}
			if item, exists := bank[a.Login]; exists && item.UserID != a.UserID {
				return fmt.Errorf("у аккаунта %s изменился Twitch user ID", a.Login)
			}
			if slices.ContainsFunc(s.state.Accounts, func(item account) bool { return item.Credentials.Login == a.Login }) || s.state.LotAccounts[a.Login].Lot != "" {
				return fmt.Errorf("аккаунт %s уже известен оркестратору. Используйте сохранённые данные", a.Login)
			}
			bank[a.Login] = *a
			byID[a.UserID] = a.Login
		}
	}
	s.state.LotCredentials = bank
	if s.persist() {
		return nil
	}
	s.state.LotCredentials = previous
	return fmt.Errorf("не удалось сохранить аккаунты для лотов")
}

func classifyManagedLot(lot managedLot, inv twitch.LotInventory) string {
	status := "ready"
	for _, reward := range lot.Rewards {
		index := slices.IndexFunc(inv.Drops, func(drop twitch.Drop) bool {
			return drop.ID == reward.ID && drop.CampaignID == reward.Campaign
		})
		benefits := lot.Benefits[reward.Campaign+"/"+reward.ID]
		if slices.ContainsFunc(benefits, func(id string) bool { return inv.ClaimedBenefits[id] }) {
			status = "claimed_unverified"
			continue
		}
		if index < 0 {
			if len(inv.ClaimedBenefits) > 0 && len(benefits) == 0 {
				// Claimed benefits can outlive their in-progress entry.
				status = "claim_unverified"
				continue
			}
			return "missing"
		}
		drop := inv.Drops[index]
		switch {
		case drop.IsClaimed:
			status = "claimed_unverified"
		case !drop.ProgressKnown || drop.Required <= 0:
			status = "claim_unverified"
		case drop.Current < drop.Required:
			return "missing"
		case drop.DropInstanceID == "":
			status = "claim_unverified"
		}
	}
	return status
}

func (s *scheduler) managedLotAccount(req managedLotRequest) (parser.Account, string) {
	var supplied *parser.Account
	if req.AccountLine != "" {
		groups, err := parser.Parse(strings.NewReader(req.AccountLine))
		if err != nil || len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].Login != req.Login {
			return parser.Account{}, "invalid_credentials"
		}
		supplied = groups[0][0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LotSold[lotAccountReceipt("login", req.Login)] {
		return parser.Account{}, "sold"
	}
	if a, ok := s.state.LotAccounts[req.Login]; ok {
		if a.Lot != req.Lot {
			return parser.Account{}, "in_another_lot"
		}
		if supplied != nil && supplied.UserID != a.Credentials.UserID {
			return parser.Account{}, "invalid_credentials"
		}
		if supplied != nil {
			return *supplied, ""
		}
		return a.Credentials, ""
	}
	if req.Operation != "inspect" && req.Operation != "preview" {
		return parser.Account{}, "not_registered"
	}
	stored, inVault := s.state.LotCredentials[req.Login]
	if inVault {
		if supplied != nil && supplied.UserID != stored.UserID {
			return parser.Account{}, "invalid_credentials"
		}
		if supplied == nil {
			supplied = &stored
		}
	}
	index := slices.IndexFunc(s.state.Accounts, func(a account) bool {
		return strings.EqualFold(a.Credentials.Login, req.Login)
	})
	if index >= 0 {
		a := s.state.Accounts[index]
		if req.Operation == "inspect" && (s.active[a.Game] != nil || s.claimJobs[a.Game] != nil) {
			return parser.Account{}, "busy_in_miner"
		}
		if supplied != nil && supplied.UserID != a.Credentials.UserID {
			return parser.Account{}, "invalid_credentials"
		}
		if supplied == nil && !a.Quarantined {
			supplied = &a.Credentials
		}
	}
	if supplied == nil {
		return parser.Account{}, "credentials_required"
	}
	if s.state.LotSold[lotAccountReceipt("id", supplied.UserID)] {
		return parser.Account{}, "sold"
	}
	if slices.ContainsFunc(s.state.Accounts, func(a account) bool {
		return a.Credentials.UserID == supplied.UserID && !strings.EqualFold(a.Credentials.Login, req.Login)
	}) {
		return parser.Account{}, "invalid_credentials"
	}
	for login, a := range s.state.LotAccounts {
		if a.Credentials.UserID == supplied.UserID && login != req.Login {
			return parser.Account{}, "in_another_lot"
		}
	}
	if req.Operation == "preview" {
		return *supplied, ""
	}
	if s.state.LotAccounts == nil {
		s.state.LotAccounts = make(map[string]lotAccount)
	}
	previous := s.state.Accounts
	if index >= 0 {
		s.state.Accounts = slices.Delete(slices.Clone(previous), index, index+1)
	}
	s.state.LotAccounts[req.Login] = lotAccount{Lot: req.Lot, Credentials: *supplied}
	if inVault {
		delete(s.state.LotCredentials, req.Login)
	}
	if !s.persist() {
		s.state.Accounts = previous
		delete(s.state.LotAccounts, req.Login)
		if inVault {
			s.state.LotCredentials[req.Login] = stored
		}
		return parser.Account{}, "storage_error"
	}
	return *supplied, ""
}

func lotAccountReceipt(lot, login string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(lot+"\x00"+login)))
}

func (s *scheduler) finishManagedLotAccount(req managedLotRequest, credentials parser.Account) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt := lotAccountReceipt(req.Lot, req.Login)
	if old := s.state.LotReceipts[receipt]; old != "" && s.state.LotAccounts[req.Login].Lot != req.Lot {
		return old
	}
	entry, ok := s.state.LotAccounts[req.Login]
	if !ok || entry.Lot != req.Lot || (req.Operation == "release" && entry.Credentials.UserID != credentials.UserID) {
		return "not_registered"
	}
	previous := s.state.Accounts
	result := "sold"
	if req.Operation == "release" {
		if slices.ContainsFunc(previous, func(a account) bool { return strings.EqualFold(a.Credentials.Login, req.Login) }) {
			return "already_in_miner"
		}
		s.state.Accounts = append(previous, account{Credentials: credentials})
		result = "returned"
	}
	delete(s.state.LotAccounts, req.Login)
	if s.state.LotReceipts == nil {
		s.state.LotReceipts = make(map[string]string)
	}
	prior := s.state.LotReceipts[receipt]
	s.state.LotReceipts[receipt] = result
	loginMarker := lotAccountReceipt("login", req.Login)
	idMarker := lotAccountReceipt("id", entry.Credentials.UserID)
	if result == "sold" {
		if s.state.LotSold == nil {
			s.state.LotSold = make(map[string]bool)
		}
		s.state.LotSold[loginMarker] = true
		s.state.LotSold[idMarker] = true
	}
	if !s.persist() {
		s.state.Accounts = previous
		s.state.LotAccounts[req.Login] = entry
		s.state.LotReceipts[receipt] = prior
		if result == "sold" {
			delete(s.state.LotSold, loginMarker)
			delete(s.state.LotSold, idMarker)
		}
		return "storage_error"
	}
	return result
}

func (s *scheduler) serveManagedLot(w http.ResponseWriter, r *http.Request) {
	if !authorizedLotRequest(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.mu.Lock()
		lot := s.state.ManagedLots[r.URL.Query().Get("lot")]
		s.mu.Unlock()
		if len(lot.Rewards) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(lot)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req managedLotRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validLotRef(req.Lot) || !validLotLogin(req.Login) ||
		(req.Operation != "inspect" && req.Operation != "preview" && req.Operation != "release" && req.Operation != "sold") ||
		(req.AccountLine != "" && req.Operation != "inspect" && req.Operation != "preview") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	req.Login = strings.ToLower(req.Login)
	reply := func(status string) { _ = json.NewEncoder(w).Encode(map[string]string{"state": status}) }
	s.mu.Lock()
	lot := s.state.ManagedLots[req.Lot]
	cfg := s.miner
	prior := s.state.LotReceipts[lotAccountReceipt(req.Lot, req.Login)]
	s.mu.Unlock()
	if req.Operation == "sold" {
		reply(s.finishManagedLotAccount(req, parser.Account{}))
		return
	}
	if (req.Operation == "inspect" || req.Operation == "preview") && len(lot.Rewards) == 0 {
		reply("lot_not_configured")
		return
	}
	credentials, status := s.managedLotAccount(req)
	if status != "" {
		if req.Operation == "release" && status == "not_registered" && prior == "returned" {
			status = prior
		}
		reply(status)
		return
	}
	cfg.Diagnostics = false
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	inventory, err := twitch.NewGQLClient(client, &cfg, &credentials).ReadLotInventory(r.Context())
	if err != nil {
		reply("inventory_unknown")
		return
	}
	if req.Operation == "inspect" || req.Operation == "preview" {
		if req.Operation == "inspect" && req.AccountLine != "" && !s.refreshLotCredentials(req, credentials) {
			reply("storage_error")
			return
		}
		reply(classifyManagedLot(lot, inventory))
		return
	}
	if !inventory.Empty {
		reply("inventory_not_empty")
		return
	}
	reply(s.finishManagedLotAccount(req, credentials))
}

func (s *scheduler) refreshLotCredentials(req managedLotRequest, credentials parser.Account) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.state.LotAccounts[req.Login]
	if entry.Lot != req.Lot || entry.Credentials.UserID != credentials.UserID {
		return false
	}
	if entry.Credentials == credentials {
		return true
	}
	s.state.LotAccounts[req.Login] = lotAccount{Lot: req.Lot, Credentials: credentials}
	if s.persist() {
		return true
	}
	s.state.LotAccounts[req.Login] = entry
	return false
}

func validLotLogin(login string) bool {
	if len(login) < 1 || len(login) > 64 {
		return false
	}
	for _, ch := range strings.ToLower(login) {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}
