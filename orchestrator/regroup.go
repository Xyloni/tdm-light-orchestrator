package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

func (s *scheduler) requestRegroup(scope string, confirm bool) string {
	var keys []string
	if scope == "all" {
		for key, g := range s.state.Games {
			if !g.Removing {
				keys = append(keys, key)
			}
		}
	} else if name, ok := strings.CutPrefix(scope, "game:"); ok {
		key := strings.ToLower(strings.TrimSpace(name))
		if g := s.state.Games[key]; g != nil && !g.Removing {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "Выберите игру или все игры."
	}
	if !confirm {
		return s.previewSchedule(scope, "current", time.Now()) + "\n\nПри подтверждении активные майнеры временно остановятся, незавершённые аккаунты будут распределены по сохранённому режиму."
	}
	changed := make([]string, 0, len(keys))
	for _, key := range keys {
		g := s.state.Games[key]
		c := g.Campaigns[g.Current]
		if !g.Regroup && c != nil && c.Finished == "" && len(c.Group) > 0 {
			g.Regroup = true
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		return "Нет сохранённых групп для перерасчёта. Ожидающие группы будут рассчитаны по текущему режиму."
	}
	if !s.persist() {
		for _, key := range changed {
			s.state.Games[key].Regroup = false
		}
		return s.notice
	}
	for _, key := range changed {
		s.event("regroup_requested game=%q mode=%q", s.state.Games[key].Name, s.state.Settings.modeName())
		if a := s.active[key]; a != nil {
			a.stopping = true
			a.process.stop()
		}
	}
	return fmt.Sprintf("Перегруппировка запрошена для %d игр. Режим: %s.", len(changed), s.state.Settings.modeName())
}

// Wait for all selected miners to exit before releasing their group plans.
func (s *scheduler) finishRegroups() {
	for key, g := range s.state.Games {
		if g.Regroup && s.active[key] != nil {
			return
		}
	}
	type previous struct {
		game     *gameState
		campaign *campaign
		group    []string
	}
	var changes []previous
	for key, g := range s.state.Games {
		if !g.Regroup {
			continue
		}
		c := g.Campaigns[g.Current]
		change := previous{game: g, campaign: c}
		if c != nil {
			change.group = c.Group
			c.Group = nil
		}
		g.Regroup = false
		delete(s.retry, key)
		changes = append(changes, change)
	}
	if len(changes) == 0 || s.persist() {
		return
	}
	for _, change := range changes {
		change.game.Regroup = true
		if change.campaign != nil {
			change.campaign.Group = change.group
		}
	}
}
