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
	Game, Login              string
	Rate, LongRate           float64
	RateKnown, LongRateKnown bool
	Recovery                 worker.ManagedRecovery
}

type laggingReport struct {
	Measured, Gathering         int
	LongMeasured, LongGathering int
	ShortLow, LongLow, BothLow  int
	Details                     []slowAccount
	Attempts                    int
}

func freshRate(known bool, rate float64, checked, now time.Time, maxAge time.Duration) bool {
	age := now.Sub(checked)
	return known && !checked.IsZero() && age >= 0 && age <= maxAge && !math.IsNaN(rate) && !math.IsInf(rate, 0)
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
		report.LongGathering += len(c.Group)
		for _, p := range a.progress {
			account := slowAccount{Game: g.Name, Login: p.Login, Recovery: p.Recovery}
			if p.Recovery.Attempts > 0 {
				report.Attempts += p.Recovery.Attempts
			}
			if terminal(p.Status) {
				report.Gathering--
				report.LongGathering--
			}
			if strings.HasPrefix(p.Status, "farm") {
				account.RateKnown = freshRate(p.RateKnown, p.Rate, p.RateChecked, now, maxAge)
				account.LongRateKnown = freshRate(p.LongRateKnown, p.LongRate, p.LongRateChecked, now, maxAge)
				account.Rate, account.LongRate = max(0, p.Rate), max(0, p.LongRate)
			}
			shortLow := account.RateKnown && account.Rate < worker.NormalWatchRate
			longLow := account.LongRateKnown && account.LongRate < worker.NormalWatchRate
			if account.RateKnown {
				report.Gathering--
				report.Measured++
			}
			if account.LongRateKnown {
				report.LongGathering--
				report.LongMeasured++
			}
			if shortLow {
				report.ShortLow++
			}
			if longLow {
				report.LongLow++
			}
			if shortLow && longLow {
				report.BothLow++
			}
			if shortLow || longLow || p.Recovery.Attempts > 0 {
				report.Details = append(report.Details, account)
			}
		}
	}
	report.Gathering = max(0, report.Gathering)
	report.LongGathering = max(0, report.LongGathering)
	sort.Slice(report.Details, func(i, j int) bool {
		a, b := report.Details[i], report.Details[j]
		if a.lowWindows() != b.lowWindows() {
			return a.lowWindows() > b.lowWindows()
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

func (a slowAccount) lowWindows() int {
	count := 0
	if a.RateKnown && a.Rate < worker.NormalWatchRate {
		count++
	}
	if a.LongRateKnown && a.LongRate < worker.NormalWatchRate {
		count++
	}
	return count
}

func (r laggingReport) text() string {
	if r.Measured+r.Gathering+r.LongMeasured+r.LongGathering == 0 {
		return "Сейчас нет активных аккаунтов для замера."
	}
	var out strings.Builder
	fmt.Fprint(&out, "⏱ Последние 10–15 минут\n")
	writeRateWindow(&out, r.Measured, r.Gathering, r.ShortLow)
	fmt.Fprint(&out, "\n\n🕒 Последние 30–35 минут\n")
	writeRateWindow(&out, r.LongMeasured, r.LongGathering, r.LongLow)
	if r.LongMeasured > 0 && r.Measured > 0 {
		fmt.Fprintf(&out, "\n\nНиже нормы в обоих окнах: %d", r.BothLow)
	}
	if r.Attempts > 0 {
		fmt.Fprintf(&out, "\nПопытки восстановления: %d", r.Attempts)
	}
	fmt.Fprint(&out, "\n\nℹ️ Норма — от 90%. Короткое окно показывает недавние колебания, длинное сглаживает их.")
	return out.String()
}

func writeRateWindow(out *strings.Builder, measured, gathering, low int) {
	if measured == 0 {
		fmt.Fprintf(out, "Ждём первый замер: %d акк.", gathering)
		return
	}
	fmt.Fprintf(out, "В норме: %d из %d с замером\nНиже нормы: %d", measured-low, measured, low)
	if gathering > 0 {
		fmt.Fprintf(out, "\nЖдём замер: %d акк.", gathering)
	}
}

func rateLabel(known bool, rate float64) string {
	if !known {
		return "ждём замер"
	}
	label := fmt.Sprintf("%.1f%%", rate*100)
	if rate < worker.NormalWatchRate && label == "90.0%" {
		return "чуть ниже 90%"
	}
	return label
}

func (r laggingReport) details(limit int) string {
	if len(r.Details) == 0 {
		return "По текущим замерам аккаунтов с пониженным темпом нет. Здесь также появятся результаты восстановления."
	}
	var out strings.Builder
	for _, a := range r.Details[:min(limit, len(r.Details))] {
		fmt.Fprintf(&out, "• %s · %s\n  10–15 мин: %s · 30–35 мин: %s\n", brief(a.Game, 40), a.Login, rateLabel(a.RateKnown, a.Rate), rateLabel(a.LongRateKnown, a.LongRate))
		if a.Recovery.Attempts > 0 {
			fmt.Fprintf(&out, "  ↳ %s\n", recoveryText(a.Recovery))
		}
		out.WriteByte('\n')
	}
	if len(r.Details) > limit {
		fmt.Fprintf(&out, "Ещё аккаунтов: %d", len(r.Details)-limit)
	}
	return strings.TrimSpace(out.String())
}
