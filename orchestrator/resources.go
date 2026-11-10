package orchestrator

import (
	"fmt"
	"strings"
	"time"
)

const resourceInterval = 30 * time.Second

type resourceValue struct {
	value float64
	known bool
}

func measured(value float64) resourceValue { return resourceValue{value, true} }

type resourceSample struct {
	at                                       time.Time
	hostCPU, containerCPU                    resourceValue
	hostRAM, containerRAM, containerLimit    resourceValue
	hostTotal, hostLoad, diskFree, diskTotal resourceValue
	readBytes, writtenBytes                  resourceValue
}

type resourceMonitor struct {
	latest  resourceSample
	history []resourceSample
	reader  resourceReader
	high    int
	alerted bool
}

func (s *scheduler) sampleResources(dir string, now time.Time) {
	m := &s.resources
	m.latest = m.reader.sample(dir, now)
	for len(m.history) > 0 && !m.history[0].at.After(now.Add(-time.Hour)) {
		m.history = m.history[1:]
	}
	m.history = append(m.history, m.latest)
	latest := m.latest
	high := (latest.hostCPU.known && latest.hostCPU.value >= 90) ||
		(latest.hostRAM.known && latest.hostTotal.known && latest.hostRAM.value >= .9*latest.hostTotal.value)
	if !s.noticeEnabled("resources") || !high {
		m.high, m.alerted = 0, false
		return
	}
	m.high++
	if m.high >= 3 && !m.alerted {
		m.alerted = true
		s.sendNotice("resources", "🖥 VPS · нагрузка 90% или выше в трёх замерах подряд.\n"+s.resourceText())
	}
}

func resourcePeak(previous, current resourceValue) resourceValue {
	if current.known && (!previous.known || current.value > previous.value) {
		return current
	}
	return previous
}

func (s *scheduler) resourceText() string {
	m := &s.resources
	x := m.latest
	if x.at.IsZero() || (!x.hostTotal.known && !x.containerRAM.known && !x.diskTotal.known) {
		return "Метрики доступны при запуске на Linux. Если оркестратор только запустился, дождитесь первого замера."
	}
	var hostCPUPeak, containerCPUPeak, hostRAMPeak, containerRAMPeak resourceValue
	for _, point := range m.history {
		hostCPUPeak = resourcePeak(hostCPUPeak, point.hostCPU)
		containerCPUPeak = resourcePeak(containerCPUPeak, point.containerCPU)
		hostRAMPeak = resourcePeak(hostRAMPeak, point.hostRAM)
		containerRAMPeak = resourcePeak(containerRAMPeak, point.containerRAM)
	}
	pct := func(x resourceValue) string {
		if !x.known {
			return "—"
		}
		return fmt.Sprintf("%.1f%%", x.value)
	}
	size := func(x resourceValue) string {
		if !x.known {
			return "—"
		}
		if x.value >= 1<<30 {
			return fmt.Sprintf("%.2f ГиБ", x.value/(1<<30))
		}
		return fmt.Sprintf("%.1f МиБ", x.value/(1<<20))
	}
	load := "—"
	if x.hostLoad.known {
		load = fmt.Sprintf("%.2f", x.hostLoad.value)
	}
	limit := "без отдельного лимита"
	if x.containerLimit.known {
		limit = size(x.containerLimit)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "🖥 VPS целиком\nCPU: %s · пик за час %s\nRAM: %s занято из %s · пик %s\nСредняя нагрузка за 15 мин: %s\nДиск с данными: %s свободно из %s",
		pct(x.hostCPU), pct(hostCPUPeak), size(x.hostRAM), size(x.hostTotal), size(hostRAMPeak), load, size(x.diskFree), size(x.diskTotal))
	fmt.Fprint(&out, "\n\n📦 Контейнер оркестратора\n")
	if x.containerRAM.known || x.containerCPU.known {
		fmt.Fprintf(&out, "CPU: %s · пик за час %s\nRAM: %s · %s · пик %s\nЧтение / запись: %s / %s за интервал\n",
			pct(x.containerCPU), pct(containerCPUPeak), size(x.containerRAM), limit, size(containerRAMPeak), size(x.readBytes), size(x.writtenBytes))
	} else {
		fmt.Fprint(&out, "Данные контейнера доступны в Docker с cgroup v2.\n")
	}
	fmt.Fprintf(&out, "Аккаунты в работе: %d\n\nℹ️ 100%% CPU контейнера = одно ядро.\n🕒 Замер в %s, интервал %d сек.", s.runningAccounts(), x.at.Local().Format("15:04:05 MST"), int(resourceInterval.Seconds()))
	return out.String()
}
