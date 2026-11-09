package worker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const NormalWatchRate = 0.90

func WatchRateMaxAge(pollIntervalSec int) time.Duration {
	return time.Duration(2*maxInt(pollIntervalSec, 60))*time.Second + time.Minute
}

func (m *Manager) statusText() string { return m.statusTextAt(time.Now()) }

// Only recent, full observation windows of actively farming accounts contribute.
// Counters and samples are already maintained by the normal Inventory polling.
func (m *Manager) statusTextAt(now time.Time) string {
	var farming, idle, errs, normal, lagging, gathering, recoveries, measured int
	var rateSum float64
	type lag struct {
		login   string
		percent float64
	}
	var slowest []lag
	maxAge := WatchRateMaxAge(m.cfg.CampaignPollIntervalSec)
	for login, st := range m.states {
		st.mu.Lock()
		status, snapshot := st.Status, st.rateSnapshot
		recoveries += st.rateRecoveries
		st.mu.Unlock()
		switch {
		case strings.HasPrefix(status, "farm"):
			farming++
			age := now.Sub(snapshot.checked)
			if !snapshot.ready || snapshot.elapsed <= 0 || age < 0 || age > maxAge {
				gathering++
				continue
			}
			percent := float64(snapshot.gain) / snapshot.elapsed.Minutes() * 100
			rateSum += percent
			measured++
			if percent >= NormalWatchRate*100 {
				normal++
			} else {
				lagging++
				slowest = append(slowest, lag{login, percent})
			}
		case strings.HasPrefix(status, "error"):
			errs++
		default:
			idle++
		}
	}
	speed := "ещё недостаточно данных"
	if measured > 0 {
		speed = fmt.Sprintf("%.1f%% по %d аккаунтам", rateSum/float64(measured), measured)
	}
	result := fmt.Sprintf("🎮 Игра: %s\n👥 Аккаунтов: %d (фармят: %d · простой: %d · ошибок: %d)\n🔢 Группа: %d/%d\n📈 Скорость начисления: %s\nОкно 10–15 мин · норма ≥90%%\n✅ Нормально: %d · отстают: %d · сбор/обновление данных: %d\n🔄 Попыток восстановления скорости: %d\n🎁 Дропов с набранным временем: %d (обнаружено)\n⏱ Аптайм: %s",
		m.cfg.TargetGame, len(m.states), farming, idle, errs, m.currentGroup.Load(), len(m.groups), speed, normal, lagging, gathering, recoveries, m.totalReady.Load(), now.Sub(m.started).Round(time.Second))
	sort.Slice(slowest, func(i, j int) bool {
		if slowest[i].percent == slowest[j].percent {
			return slowest[i].login < slowest[j].login
		}
		return slowest[i].percent < slowest[j].percent
	})
	if len(slowest) > 5 {
		slowest = slowest[:5]
	}
	if len(slowest) > 0 {
		items := make([]string, 0, len(slowest))
		for _, s := range slowest {
			login := s.login
			if len(login) > 25 {
				login = login[:25]
			}
			items = append(items, fmt.Sprintf("%s (%.1f%%)", login, s.percent))
		}
		result += "\nОтстающие: " + strings.Join(items, ", ") + "\nПодробнее: /logs <логин>"
	}
	return result
}
