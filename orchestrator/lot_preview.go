package orchestrator

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type lotReward struct {
	Campaign string
	ID       string
	Name     string
}

type lotInventory struct {
	Missing   []lotReward
	Unclaimed []lotReward
	Claimed   int
}

func inspectLotInventory(required []lotReward, drops []twitch.Drop) lotInventory {
	inventory := make(map[[2]string]twitch.Drop, len(drops))
	for _, drop := range drops {
		inventory[[2]string{drop.CampaignID, drop.ID}] = drop
	}
	var result lotInventory
	for _, reward := range required {
		drop, found := inventory[[2]string{reward.Campaign, reward.ID}]
		switch {
		case found && drop.IsClaimed:
			result.Claimed++
		case found && drop.ProgressKnown && drop.Required > 0 && drop.Current >= drop.Required:
			result.Unclaimed = append(result.Unclaimed, reward)
		default:
			result.Missing = append(result.Missing, reward)
		}
	}
	return result
}

func (s *scheduler) serveLotPreview(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("ORCHESTRATOR_LOT_PREVIEW_TOKEN")
	value, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	expected, actual := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(value))
	if len(token) < 32 || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Game, Login string
		Campaigns   []string
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Campaigns) == 0 || len(req.Campaigns) > 20 || req.Login == "" {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	g := s.state.Games[strings.ToLower(strings.TrimSpace(req.Game))]
	cfg := s.miner
	var required []lotReward
	valid := g != nil && !g.Removing
	if valid {
		seen := make(map[[2]string]bool)
		for _, id := range req.Campaigns {
			c := g.Campaigns[id]
			if c == nil || len(c.Rewards) == 0 {
				valid = false
				break
			}
			for _, reward := range c.Rewards {
				if reward.ID == "" {
					valid = false
					break
				}
				key := [2]string{c.ID, reward.ID}
				if !seen[key] {
					required = append(required, lotReward{c.ID, reward.ID, reward.Name})
					seen[key] = true
				}
			}
		}
	}
	var credentials *parser.Account
	for _, a := range s.state.Accounts {
		if strings.EqualFold(a.Credentials.Login, req.Login) && !a.Quarantined {
			copy := a.Credentials
			credentials = &copy
			break
		}
	}
	s.mu.Unlock()
	if !valid || len(required) == 0 {
		http.Error(w, "campaign or reward catalog unavailable", http.StatusUnprocessableEntity)
		return
	}
	if credentials == nil {
		http.Error(w, "account unavailable in orchestrator", http.StatusNotFound)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	drops, err := twitch.NewGQLClient(client, &cfg, credentials).DropsDashboard(r.Context())
	if err != nil {
		http.Error(w, "Twitch inventory unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(inspectLotInventory(required, drops))
}
