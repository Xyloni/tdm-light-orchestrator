package orchestrator

import (
	"fmt"
	"sort"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

const catalogAlertThreshold = 3

type catalogHealth struct {
	LastSuccess time.Time
	Failures    int
}

func (s *scheduler) event(format string, args ...any) {
	if s.journal != nil {
		if err := s.journal.Output(2, fmt.Sprintf(format, args...)); err != nil {
			s.notice = "Не удалось записать журнал решений."
		}
	}
}

func (s *scheduler) catalogSummary() string {
	health := s.state.Catalog
	message := "Последний успешный опрос Twitch: пока не было."
	if !health.LastSuccess.IsZero() {
		message = "Последний успешный опрос Twitch: " + health.LastSuccess.Local().Format("02.01.2006 15:04:05 MST") + "."
	}
	if health.Failures > 0 {
		message += fmt.Sprintf(" Неудачных опросов подряд: %d.", health.Failures)
	}
	if s.discoveryError != "" {
		message += " " + s.discoveryError
	}
	return message
}

func (s *scheduler) catalogFailed() {
	s.state.Catalog.Failures++
	s.persist()
	s.event("catalog_failed consecutive=%d", s.state.Catalog.Failures)
	if s.state.Catalog.Failures == catalogAlertThreshold {
		s.sendNotice("catalog", fmt.Sprintf("Twitch не вернул каталог %d раза подряд. Поиск продолжится автоматически.", catalogAlertThreshold))
	}
}

func (s *scheduler) catalogSucceeded(now time.Time) {
	previous := s.state.Catalog.Failures
	s.state.Catalog = catalogHealth{LastSuccess: now}
	if previous > 0 {
		s.event("catalog_recovered consecutive_failures=%d", previous)
	}
	if previous >= catalogAlertThreshold {
		s.sendNotice("catalog", "Каталог Twitch снова доступен. Автоматический поиск кампаний восстановлен.")
	}
}

// Use the lower quartile and blend only when Twitch reported new measurements.
func updateRate(c *campaign, a *activeGame, progress []worker.ManagedProgress, baseline float64) {
	newest := a.rateChecked
	rates := make([]float64, 0, len(progress))
	for _, p := range progress {
		if !p.RateKnown || p.RateChecked.IsZero() || terminal(p.Status) {
			continue
		}
		rates = append(rates, max(0.05, min(1, p.Rate)))
		if p.RateChecked.After(newest) {
			newest = p.RateChecked
		}
	}
	if len(rates) == 0 || (!a.rateChecked.IsZero() && newest.Sub(a.rateChecked) < time.Minute) {
		return
	}
	sort.Float64s(rates)
	previous := c.ObservedRate
	if previous <= 0 {
		previous = baseline
	}
	c.ObservedRate = 0.75*previous + 0.25*rates[len(rates)/4]
	a.rateChecked = newest
}
