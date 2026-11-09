package orchestrator

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestCatalogAlertsOnceAndReportsRecovery(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	var messages []string
	s.notify = func(message string) { messages = append(messages, message) }
	for i := 0; i < 5; i++ {
		s.applyCatalog(discoveryResult{err: errors.New("unavailable")}, now)
	}
	if len(messages) != 1 || s.state.Catalog.Failures != 5 {
		t.Fatal("expected one failure notification")
	}
	s.applyCatalog(discoveryResult{}, now)
	s.applyCatalog(discoveryResult{}, now)
	saved, err := loadState(s.path)
	if err != nil || len(messages) != 2 || saved.Catalog.Failures != 0 || !saved.Catalog.LastSuccess.Equal(now) {
		t.Fatalf("catalog recovery did not persist or notify: %v", err)
	}
}

func TestRateIgnoresSingleOutlierAndRecovers(t *testing.T) {
	now := time.Now()
	c := &campaign{}
	a := &activeGame{}
	sample := func(at time.Time, rates ...float64) []worker.ManagedProgress {
		var result []worker.ManagedProgress
		for _, rate := range rates {
			result = append(result, worker.ManagedProgress{RateKnown: true, Rate: rate, RateChecked: at})
		}
		return result
	}
	updateRate(c, a, sample(now, 0.05, 0.8, 0.8, 0.8), 0.8)
	if math.Abs(c.ObservedRate-0.8) > 1e-9 {
		t.Fatalf("outlier pulled down the estimate: %v", c.ObservedRate)
	}
	updateRate(c, a, sample(now.Add(time.Minute), 0.4, 0.4, 0.4, 0.4), 0.8)
	lowered := c.ObservedRate
	updateRate(c, a, sample(now.Add(time.Minute), 1, 1, 1, 1), 0.8)
	if c.ObservedRate != lowered {
		t.Fatal("reused an already processed sample")
	}
	updateRate(c, a, sample(now.Add(2*time.Minute), 0.8, 0.8, 0.8, 0.8), 0.8)
	if lowered >= 0.8 || c.ObservedRate <= lowered {
		t.Fatal("rate did not fall and recover")
	}
}
