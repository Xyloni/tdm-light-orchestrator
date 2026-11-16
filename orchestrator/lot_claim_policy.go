package orchestrator

import (
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/config"
)

// The caller holds the scheduler lock. Claiming a Drop on Twitch does not
// establish fulfillment for a future game account, so saved lot campaigns
// retain their ready rewards in the Twitch inventory.
func (s *scheduler) lotClaimProtected(game, campaign string) bool {
	for _, lot := range s.state.ManagedLots {
		if !strings.EqualFold(lot.Game, game) {
			continue
		}
		for _, reward := range lot.Rewards {
			if campaign == "" || reward.Campaign == campaign {
				return true
			}
		}
	}
	return false
}

func (s *scheduler) configForCampaign(g *gameState, campaign string) config.Config {
	cfg := s.configForGame(g)
	cfg.CampaignID = campaign
	cfg.TargetGame = g.Name
	cfg.AutoClaimDrops = cfg.AutoClaimDrops && !s.lotClaimProtected(g.Name, campaign)
	return cfg
}

func (s *scheduler) activeLotClaimPolicies() map[string]bool {
	policies := make(map[string]bool)
	for key, active := range s.active {
		g := s.state.Games[key]
		if g != nil && g.AutoClaimDrops && g.Current != "" && !active.stopping {
			policies[key] = s.lotClaimProtected(g.Name, g.Current)
		}
	}
	return policies
}

func (s *scheduler) refreshLotClaimPolicies(previous map[string]bool) {
	for key, wasProtected := range previous {
		g := s.state.Games[key]
		protected := s.lotClaimProtected(g.Name, g.Current)
		if protected == wasProtected {
			continue
		}
		a := s.active[key]
		a.stopping = true
		a.process.stop()
		s.event("lot_claim_policy_changed game=%q campaign=%q protected=%t", g.Name, g.Current, protected)
	}
}
