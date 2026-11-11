package orchestrator

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
)

type rewardRequirement struct {
	ID      string
	Name    string
	Minutes int
}

func (s *scheduler) configForGame(g *gameState) config.Config {
	cfg := s.miner
	cfg.AutoClaimDrops = g.AutoClaimDrops
	cfg.IgnoredDropNames = append(append([]string(nil), cfg.IgnoredDropNames...), g.ExcludedRewards...)
	return cfg
}

func requiredMinutes(rewards []rewardRequirement, cfg config.Config) int {
	threshold := 0
	for _, reward := range rewards {
		if !cfg.DropIgnored(reward.Name) {
			threshold = max(threshold, reward.Minutes)
		}
	}
	return threshold
}

// The caller holds the scheduler lock.
func (s *scheduler) changeExclusion(key, name string, exclude bool) string {
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра не найдена."
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 300 || strings.ContainsAny(name, "\r\n\t") {
		return "Укажите полное название награды."
	}
	if s.miner.DropIgnored(name) {
		return "Эта награда исключена общей конфигурацией майнеров."
	}
	next := make([]string, 0, len(g.ExcludedRewards)+1)
	found := false
	for _, old := range g.ExcludedRewards {
		if strings.EqualFold(old, name) {
			found = true
			continue
		}
		next = append(next, old)
	}
	if found == exclude {
		if exclude {
			return "Награда уже исключена для этой игры."
		}
		return "Награды нет в списке исключений игры."
	}
	if exclude {
		next = append(next, name)
	}
	before := g.ExcludedRewards
	g.ExcludedRewards = next
	cfg := s.configForGame(g)
	now := time.Now()
	backups := make(map[*campaign]campaign)
	for _, c := range g.Campaigns {
		if !now.Before(c.Ends) || len(c.Rewards) == 0 {
			continue
		}
		minutes := requiredMinutes(c.Rewards, cfg)
		restored := !exclude && slices.ContainsFunc(c.Rewards, func(reward rewardRequirement) bool {
			return strings.EqualFold(strings.TrimSpace(reward.Name), name)
		})
		if c.Minutes == minutes && !restored {
			continue
		}
		backups[c] = *c
		if restored {
			// Accounts previously finished under the old list need an Inventory recheck.
			results := make(map[string]string, len(c.Results))
			for login, outcome := range c.Results {
				if outcome != "idle" {
					results[login] = outcome
				}
			}
			c.Results = results
			c.Finished = ""
		}
		c.Minutes = minutes
	}
	if !s.persist() {
		g.ExcludedRewards = before
		for c, old := range backups {
			*c = old
		}
		return s.notice
	}
	s.lastDiscovery = time.Time{}
	if a := s.active[key]; a != nil {
		a.stopping = true
		a.discardResult = true
		a.process.stop()
	}
	s.event("reward_exclusion game=%q reward=%q excluded=%t", g.Name, name, exclude)
	if exclude {
		return fmt.Sprintf("«%s» исключена для «%s».", name, g.Name)
	}
	return fmt.Sprintf("«%s» снова учитывается для «%s».", name, g.Name)
}

func (s *scheduler) exclusionList(key string) string {
	g := s.state.Games[key]
	if g == nil {
		return "Игра не найдена."
	}
	if len(g.ExcludedRewards) == 0 {
		return "У игры нет отдельных исключений."
	}
	return "Исключения для «" + g.Name + "»:\n• " + strings.Join(g.ExcludedRewards, "\n• ")
}
