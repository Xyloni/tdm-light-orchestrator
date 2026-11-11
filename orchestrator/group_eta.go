package orchestrator

import (
	"fmt"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func (s *scheduler) activeGroupETA(key string, c *campaign, now time.Time) string {
	a := s.active[key]
	if a == nil || a.stopping || a.groupStarted.IsZero() {
		return ""
	}
	air := s.airtimeFor(key, c.ID)
	if air == nil || len(air.streams) == 0 {
		return "Финиш текущей группы: ждём подтверждённый эфир."
	}
	progress := make(map[string]worker.ManagedProgress, len(a.progress))
	for _, p := range a.progress {
		progress[p.Login] = p
	}
	var eta time.Duration
	measured, remaining := 0, 0
	for _, login := range c.Group {
		p := progress[login]
		if c.Results[login] != "" || terminal(p.Status) {
			continue
		}
		remaining++
		if p.ProgressKnown && p.Required > 0 {
			measured++
			eta = max(eta, s.accountFinishETA(c, p, 0, now))
		}
	}
	if remaining == 0 {
		return "Финиш текущей группы: завершается."
	}
	if measured < remaining {
		return fmt.Sprintf("Финиш текущей группы: ждём прогресс Twitch (%d из %d).", measured, remaining)
	}
	end := now.Add(eta)
	text := "Финиш текущей группы: около " + end.Local().Format("02.01 в 15:04 MST")
	if end.After(c.Ends) {
		text += " · позже окончания кампании"
	}
	return text
}
