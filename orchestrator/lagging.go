package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

type slowAccount struct {
	Game, Login string
	Rate        float64
	Recovery    worker.ManagedRecovery
}

type laggingReport struct {
	Measured, Gathering int
	Slow                []slowAccount
	Recoveries          []slowAccount
	Attempts            int
}

func (r laggingReport) Share() float64 {
	if r.Measured == 0 {
		return 0
	}
	return float64(len(r.Slow)) * 100 / float64(r.Measured)
}

func (r laggingReport) AverageDeficit() float64 {
	if len(r.Slow) == 0 {
		return 0
	}
	total := 0.0
	for _, account := range r.Slow {
		total += (1 - account.Rate) * 100
	}
	return total / float64(len(r.Slow))
}

func (s *scheduler) laggingReport(key string, now time.Time) laggingReport {
	var report laggingReport
	maxAge := worker.WatchRateMaxAge(s.miner.CampaignPollIntervalSec)
	for game, a := range s.active {
		if (key != "" && game != key) || a.stopping || a.groupStarted.IsZero() {
			continue
		}
		g := s.state.Games[game]
		if g == nil || g.Paused || g.Removing {
			continue
		}
		c := g.Campaigns[g.Current]
		if c == nil {
			continue
		}
		report.Gathering += len(c.Group)
		for _, p := range a.progress {
			if p.Recovery.Attempts > 0 {
				report.Attempts += p.Recovery.Attempts
				report.Recoveries = append(report.Recoveries, slowAccount{Game: g.Name, Login: p.Login, Recovery: p.Recovery})
			}
			if terminal(p.Status) {
				report.Gathering--
				continue
			}
			age := now.Sub(p.RateChecked)
			if !strings.HasPrefix(p.Status, "farm") || !p.RateKnown || p.RateChecked.IsZero() || age < 0 || age > maxAge || math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) {
				continue
			}
			report.Gathering--
			report.Measured++
			if p.Rate < worker.NormalWatchRate {
				report.Slow = append(report.Slow, slowAccount{Game: g.Name, Login: p.Login, Rate: max(0, p.Rate), Recovery: p.Recovery})
			}
		}
	}
	report.Gathering = max(0, report.Gathering)
	sort.Slice(report.Slow, func(i, j int) bool {
		a, b := report.Slow[i], report.Slow[j]
		if a.Rate != b.Rate {
			return a.Rate < b.Rate
		}
		if a.Game != b.Game {
			return a.Game < b.Game
		}
		return a.Login < b.Login
	})
	sort.SliceStable(report.Recoveries, func(i, j int) bool {
		a, b := report.Recoveries[i], report.Recoveries[j]
		if !a.Recovery.At.Equal(b.Recovery.At) {
			return a.Recovery.At.After(b.Recovery.At)
		}
		if a.Game != b.Game {
			return a.Game < b.Game
		}
		return a.Login < b.Login
	})
	return report
}

func recoveryText(recovery worker.ManagedRecovery) string {
	status := "нет полного замера"
	switch recovery.State {
	case "running":
		status = "восстановление идёт"
	case "measuring":
		status = "ждём полный замер после попытки"
	case "failed":
		status = "смена канала или переподключение не удались"
	case "normal":
		status = fmt.Sprintf("темп вернулся к норме: %.1f%%", recovery.Rate*100)
	case "still_slow":
		status = fmt.Sprintf("после попытки темп всё ещё ниже нормы: %.1f%%", recovery.Rate*100)
	}
	return fmt.Sprintf("попыток: %d · %s", recovery.Attempts, status)
}

func (r laggingReport) text() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Проверено: %d · ждём замеры: %d\nОтстают: %d", r.Measured, r.Gathering, len(r.Slow))
	if r.Measured > 0 {
		fmt.Fprintf(&out, " (%.1f%% от проверенных)", r.Share())
	}
	listed := make(map[[2]string]bool)
	if len(r.Slow) > 0 {
		fmt.Fprintf(&out, "\nСреднее отставание: %.1f%%\n\nСамые медленные:", r.AverageDeficit())
		for _, a := range r.Slow[:min(10, len(r.Slow))] {
			fmt.Fprintf(&out, "\n• %s · %s: темп %.1f%%, отставание %.1f%%", brief(a.Game, 40), a.Login, a.Rate*100, (1-a.Rate)*100)
			if a.Recovery.Attempts > 0 {
				fmt.Fprintf(&out, "\n  ↳ %s", recoveryText(a.Recovery))
				listed[[2]string{a.Game, a.Login}] = true
			}
		}
		if len(r.Slow) > 10 {
			fmt.Fprintf(&out, "\nЕщё отстающих: %d.", len(r.Slow)-10)
		}
	} else if r.Measured == 0 {
		fmt.Fprint(&out, "\nОценка появится после первого полного окна наблюдения.")
	}
	if r.Attempts > 0 {
		fmt.Fprintf(&out, "\n\nВосстановление скорости: %d попыток в текущих группах.", r.Attempts)
		shown := 0
		for _, a := range r.Recoveries {
			if listed[[2]string{a.Game, a.Login}] {
				continue
			}
			if shown == 5 {
				break
			}
			fmt.Fprintf(&out, "\n• %s · %s: %s", brief(a.Game, 32), a.Login, recoveryText(a.Recovery))
			shown++
		}
	}
	fmt.Fprint(&out, "\n\n100% = 1 минута Twitch за минуту просмотра. Отстают при темпе ниже 90%. Окно замера: 10–15 минут.")
	return out.String()
}
