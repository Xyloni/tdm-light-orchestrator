package orchestrator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type simulationJob struct {
	Name                      string
	Accounts                  int
	Ready, Deadline, Duration time.Duration
	Reserve                   time.Duration
	Economy                   bool
	Occupied                  int // Existing work outside the selected preview scope.
}

type simulationResult struct {
	Job     simulationJob
	Groups  []int
	Started time.Duration
	Finish  time.Duration
	Missed  int
}

func simulateSchedule(jobs []simulationJob, maxGames, maxAccounts int) ([]simulationResult, int, bool) {
	type run struct {
		left, inFlight int
		until          time.Duration
		slot           bool
	}
	results := make([]simulationResult, len(jobs))
	running := make([]run, len(jobs))
	for i, job := range jobs {
		results[i].Job = job
		running[i] = run{left: job.Accounts, inFlight: job.Occupied, until: job.Duration, slot: job.Occupied > 0}
	}
	peak, now := 0, time.Duration(0)
	for step := 0; step < 10000; step++ {
		slots, load := 0, 0
		var latestRelease time.Duration
		for i := range running {
			r, job, result := &running[i], jobs[i], &results[i]
			if r.inFlight > 0 && r.until <= now {
				r.inFlight = 0
			}
			if r.inFlight == 0 && (r.left == 0 || job.Deadline <= now) {
				result.Missed += r.left
				r.left, r.slot = 0, false
			}
			if r.slot {
				slots++
			}
			load += r.inFlight
			if r.inFlight > 0 {
				latestRelease = max(latestRelease, min(r.until, job.Deadline))
			}
		}
		peak = max(peak, load)
		candidates := make([]int, 0, len(jobs))
		for i, r := range running {
			if r.inFlight == 0 && r.left > 0 && jobs[i].Ready <= now && jobs[i].Deadline > now {
				candidates = append(candidates, i)
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if running[a].slot != running[b].slot {
				return running[a].slot
			}
			latest := func(n int) time.Duration {
				return jobs[n].Deadline - jobs[n].Reserve - time.Duration((running[n].left-1)/maxAccounts+1)*jobs[n].Duration
			}
			return latest(a) < latest(b)
		})
		for _, i := range candidates {
			r, job, result := &running[i], jobs[i], &results[i]
			if !r.slot && slots >= maxGames {
				continue
			}
			left := job.Deadline - now
			count := planGroup(r.left, load, maxAccounts, left-job.Reserve, job.Duration, job.Economy)
			normalWaves := (r.left-1)/maxAccounts + 1
			if count > max(0, maxAccounts-load) && latestRelease > now && (job.Deadline-job.Reserve-latestRelease)/job.Duration >= time.Duration(normalWaves) {
				continue
			}
			if !r.slot {
				r.slot = true
				slots++
			}
			if len(result.Groups) == 0 {
				result.Started = now
			}
			result.Groups = append(result.Groups, count)
			if job.Duration > left {
				result.Missed += count
			}
			r.left -= count
			r.inFlight = count
			r.until = now + min(left, job.Duration)
			result.Finish = r.until
			load += count
			latestRelease = max(latestRelease, r.until)
			peak = max(peak, load)
		}
		var next time.Duration
		advance := func(at time.Duration) {
			if at > now && (next == 0 || at < next) {
				next = at
			}
		}
		for i, r := range running {
			if r.inFlight > 0 {
				advance(r.until)
			}
			if r.left > 0 {
				advance(jobs[i].Ready)
				advance(jobs[i].Deadline)
			}
		}
		if next == 0 {
			return results, peak, true
		}
		now = next
	}
	return results, peak, false
}

func formatSimulation(jobs []simulationJob, maxGames, maxAccounts int, now time.Time) string {
	if len(jobs) == 0 {
		return "Нет активных или предстоящих кампаний с доступными аккаунтами для прогноза."
	}
	results, peak, complete := simulateSchedule(jobs, maxGames, maxAccounts)
	var out strings.Builder
	fmt.Fprintf(&out, "📊 Нагрузка плана\nПик аккаунтов: %d / %d", peak, maxAccounts)
	if !complete {
		fmt.Fprint(&out, "\n⚠️ Показан частичный результат.")
	}
	for _, r := range results {
		if r.Job.Occupied > 0 {
			continue
		}
		mode := "Скорость"
		if r.Job.Economy {
			mode = "Экономия ресурсов"
		}
		fmt.Fprintf(&out, "\n\n🎮 %s\nРежим: %s\nАккаунты: %d", brief(r.Job.Name, 60), mode, r.Job.Accounts)
		if len(r.Groups) > 0 {
			var chunks []string
			for i := 0; i < len(r.Groups) && len(chunks) < 5; {
				j := i + 1
				for j < len(r.Groups) && r.Groups[j] == r.Groups[i] {
					j++
				}
				chunks = append(chunks, fmt.Sprintf("%d×%d", j-i, r.Groups[i]))
				i = j
				if i < len(r.Groups) && len(chunks) == 5 {
					chunks = append(chunks, "…")
				}
			}
			fmt.Fprintf(&out, "\nГруппы: %d (%s)", len(r.Groups), strings.Join(chunks, " + "))
			if now.IsZero() {
				fmt.Fprintf(&out, "\nСтарт: +%s\nФиниш: +%s", r.Started, r.Finish)
			} else {
				fmt.Fprintf(&out, "\nСтарт: %s\nФиниш: %s", now.Add(r.Started).Format("02.01 в 15:04 MST"), now.Add(r.Finish).Format("02.01 в 15:04 MST"))
			}
			if r.Job.Reserve > 0 {
				fmt.Fprintf(&out, "\nРезерв кампании: %d мин", r.Job.Reserve/time.Minute)
			}
			remaining := (r.Job.Deadline - r.Finish - r.Job.Reserve).Round(time.Minute)
			if remaining >= 0 {
				fmt.Fprintf(&out, "\nДо дедлайна сверх резерва: %s", remaining)
			} else {
				fmt.Fprintf(&out, "\n⚠️ Резерва не хватает: %s", -remaining)
			}
		}
		if r.Missed > 0 {
			fmt.Fprintf(&out, "\n⚠️ Риск дедлайна: %d аккаунтов", r.Missed)
		}
	}
	return out.String()
}

func (s *scheduler) previewSchedule(scope, mode string, now time.Time) string {
	if mode != "" && mode != "current" && mode != "speed" && mode != "resources" {
		return "Выберите current, speed или resources."
	}
	key, one := strings.CutPrefix(scope, "game:")
	if scope != "all" && (!one || s.state.Games[key] == nil) {
		return "Выберите игру или all."
	}
	keys := make([]string, 0, len(s.state.Games))
	for k := range s.state.Games {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var jobs []simulationJob
	free := 0
	for _, a := range s.state.Accounts {
		if a.Game == "" && !a.Quarantined {
			free++
		}
	}
	for _, k := range keys {
		g := s.state.Games[k]
		if g.Paused || g.Removing {
			continue
		}
		if one && k != key {
			a := s.active[k]
			c := g.Campaigns[g.Current]
			if a != nil && c != nil && len(c.Group) > 0 && !a.groupStarted.IsZero() && c.Ends.After(now) {
				duration := max(time.Second, min(c.Ends.Sub(now), a.groupStarted.Add(s.duration(g, c)).Sub(now)))
				jobs = append(jobs, simulationJob{Name: g.Name, Occupied: len(c.Group), Duration: duration, Deadline: duration})
			}
			continue
		}
		c := g.Campaigns[g.Current]
		if c == nil || !campaignReady(c, now) {
			c = s.nextCampaign(g, now)
		}
		if c == nil {
			for _, candidate := range g.Campaigns {
				if candidate.Finished == "" && !allRewardsExcluded(candidate) && candidate.Starts.After(now) &&
					(c == nil || candidate.Starts.Before(c.Starts)) {
					c = candidate
				}
			}
		}
		if c == nil {
			continue
		}
		accounts := len(pending(s.state, k, c))
		usable := 0
		for _, a := range s.state.Accounts {
			if a.Game == k && !a.Quarantined {
				usable++
			}
		}
		assigned := min(free, max(0, g.Target-usable))
		free -= assigned
		accounts += assigned
		if accounts == 0 {
			continue
		}
		cfg := s.gameSettings(g)
		if (one || g.Mode == "") && (mode == "speed" || mode == "resources") {
			cfg.SaveResources = mode == "resources"
		}
		jobs = append(jobs, simulationJob{Name: g.Name, Accounts: accounts, Ready: max(0, c.Starts.Sub(now)), Deadline: c.Ends.Sub(now), Duration: s.duration(g, c), Reserve: time.Duration(cfg.ReserveMinutes) * time.Minute, Economy: cfg.SaveResources})
	}
	if one {
		found := false
		for _, job := range jobs {
			if job.Accounts > 0 {
				found = true
				break
			}
		}
		if !found {
			return "У выбранной игры нет доступной кампании и аккаунтов для прогноза либо она на паузе."
		}
	}
	return formatSimulation(jobs, s.state.Settings.MaxGames, s.state.Settings.MaxAccounts, now.Local()) + "\n\nℹ️ Расчёт с нуля. Активные группы не меняются."
}

func simulateInput(input string) string {
	if strings.TrimSpace(input) == "" {
		input = "1 20 | 30 30 90 | 20 45 120"
	}
	parts := strings.Split(input, "|")
	invalid := "Формат: слоты лимит | аккаунты минуты_на_группу минуты_до_дедлайна | … Например: 1 20 | 30 30 90 | 20 45 120"
	if len(parts) < 2 || len(parts) > 7 {
		return invalid
	}
	values := func(part string, n int) []int {
		fields := strings.Fields(part)
		if len(fields) != n {
			return nil
		}
		result := make([]int, n)
		for i, f := range fields {
			x, err := strconv.Atoi(f)
			if err != nil || x < 1 || x > 10000 {
				return nil
			}
			result[i] = x
		}
		return result
	}
	limits := values(parts[0], 2)
	if limits == nil {
		return invalid
	}
	jobs := make([]simulationJob, 0, len(parts)-1)
	for i, part := range parts[1:] {
		v := values(part, 3)
		if v == nil {
			return invalid
		}
		jobs = append(jobs, simulationJob{Name: fmt.Sprintf("Тестовая игра %d", i+1), Accounts: v[0], Duration: time.Duration(v[1]) * time.Minute, Deadline: time.Duration(v[2]) * time.Minute, Economy: true})
	}
	return "Симуляция · режим экономии, старт отсчёта +0.\n" + formatSimulation(jobs, limits[0], limits[1], time.Time{})
}
