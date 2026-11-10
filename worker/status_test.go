package worker

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func statusTestManager(now time.Time) *Manager {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	return &Manager{cfg: cfg, states: map[string]*AccountState{}, started: now.Add(-time.Hour)}
}

func TestStatusUsesOnlyFreshActiveRateWindows(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	m := statusTestManager(now)
	for _, tc := range []struct {
		login, status string
		gain          int
		age           time.Duration
	}{
		{"normal", "farm: campaign", 10, 0},
		{"slow", "farm: campaign", 6, 0},
		{"stale", "farm: campaign", 0, 6 * time.Minute},
		{"idle", "idle", 500, 0},
		{"auth", "error: auth", 500, 0},
	} {
		st := &AccountState{Status: tc.status, Account: &parser.Account{Login: tc.login}}
		var rate watchRate
		end := now.Add(-tc.age)
		rate.reset(end.Add(-10*time.Minute), 0, true)
		for i := 1; i <= 5; i++ {
			rate.observe(end.Add(time.Duration(i*2-10)*time.Minute), tc.gain*i/5, true, 5*time.Minute)
		}
		st.setWatchRates(&rate, &watchRate{})
		m.states[tc.login] = st
	}
	m.states["warming"] = &AccountState{Status: "farm: campaign"}
	m.states["slow"].rateRecoveries = 3
	m.totalReady.Store(2)
	text := m.statusTextAt(now)
	for _, want := range []string{"фармят: 4 · простой: 1 · ошибок: 1", "80.0% по 2 аккаунтам", "Нормально: 1 · отстают: 1 · сбор/обновление данных: 2", "восстановления скорости: 3", "Отстающие: slow (60.0%)", "Дропов с набранным временем: 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	m.states["slow"].clearWatchRate()
	if text = m.statusTextAt(now); !strings.Contains(text, "100.0% по 1 аккаунтам") || strings.Contains(text, "Отстающие:") {
		t.Fatal(text)
	}
}

func TestStatusEmptyAnd500AccountsRemainCompact(t *testing.T) {
	now := time.Now()
	m := statusTestManager(now)
	if text := m.statusTextAt(now); !strings.Contains(text, "ещё недостаточно данных") || strings.Contains(text, "NaN") {
		t.Fatal(text)
	}
	for i := 0; i < 500; i++ {
		login := fmt.Sprintf("user%03d", i)
		m.states[login] = &AccountState{Status: "farm", rateSnapshot: rateSnapshot{gain: 5, elapsed: 10 * time.Minute, checked: now, ready: true}}
	}
	text := m.statusTextAt(now)
	if len(text) > 4000 || !strings.Contains(text, "отстают: 500") || !strings.Contains(text, "user004") || strings.Contains(text, "user005") {
		t.Fatal(text)
	}
}

func TestStatusConcurrentRateUpdates(t *testing.T) {
	now := time.Now()
	m := statusTestManager(now)
	st := &AccountState{Status: "farm"}
	m.states["test"] = st
	r := watchRate{samples: []progressSample{{now.Add(-10 * time.Minute), 0}, {now.Add(-8 * time.Minute), 2}, {now.Add(-4 * time.Minute), 6}, {now, 10}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				st.setWatchRates(&r, &watchRate{})
				_ = m.statusTextAt(now)
				st.clearWatchRate()
			}
		}()
	}
	wg.Wait()
}

func TestManagedSnapshotPublishesSeparateRateWindows(t *testing.T) {
	start := time.Now()
	m := statusTestManager(start)
	st := &AccountState{Status: "farm", Account: &parser.Account{Login: "viewer"}}
	m.states["viewer"] = st
	var short, long watchRate
	for i := 0; i <= 15; i++ {
		at := start.Add(time.Duration(2*i) * time.Minute)
		minutes := i + max(0, i-10)
		short.observe(at, minutes, true, 5*time.Minute)
		long.observeWindow(at, minutes, true, 5*time.Minute, sustainedRateWindow, sustainedRateMaxWindow, NormalWatchRate)
		if i == 5 {
			st.setWatchRates(&short, &long)
			p := m.ManagedSnapshot()[0]
			if !p.RateKnown || p.LongRateKnown {
				t.Fatalf("expected only a short measurement, got %+v", p)
			}
		}
	}
	st.setWatchRates(&short, &long)
	p := m.ManagedSnapshot()[0]
	if !p.RateKnown || p.Rate != 1 || !p.LongRateKnown || p.LongRate != 20.0/30 || !p.LongRateChecked.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("unexpected rate measurements: %+v", p)
	}
	st.clearWatchRate()
	p = m.ManagedSnapshot()[0]
	if p.RateKnown || p.LongRateKnown {
		t.Fatalf("rate measurements survived a reset: %+v", p)
	}
}
