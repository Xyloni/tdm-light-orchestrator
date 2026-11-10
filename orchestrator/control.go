package orchestrator

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func (s *scheduler) command(input string) string {
	action, rest, _ := strings.Cut(strings.TrimSpace(input), " ")
	return s.operate(action, strings.TrimSpace(rest))
}

func (s *scheduler) operate(action, rest string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case "", "help":
		return commandHelp(rest)
	case "preview":
		name, mode, _ := strings.Cut(rest, "|")
		scope := strings.ToLower(strings.TrimSpace(name))
		if scope != "all" {
			scope = "game:" + scope
		}
		return s.previewSchedule(scope, strings.TrimSpace(mode), time.Now())
	case "simulate":
		return simulateInput(rest)
	case "digest":
		return s.setDigest(rest)
	case "stats":
		return s.statsText(rest, time.Now())
	case "lagging":
		key := strings.ToLower(rest)
		if key != "" && s.state.Games[key] == nil {
			return "Игра не найдена."
		}
		return s.laggingReport(key, time.Now()).text()
	case "status":
		return s.status()
	case "errors":
		return s.accountErrors(strings.ToLower(rest))
	case "add", "target":
		number, name, _ := strings.Cut(rest, " ")
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		target, err := strconv.Atoi(number)
		if name == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n\t") || err != nil || target < 1 {
			return "Укажите положительное число и название игры."
		}
		g := s.state.Games[key]
		if action == "add" {
			if g != nil {
				return "Игра уже добавлена. Используйте target для изменения цели."
			}
			s.state.Games[key] = &gameState{Name: name, Target: target, Campaigns: make(map[string]*campaign)}
			if !s.persist() {
				delete(s.state.Games, key)
				return s.notice
			}
			s.lastDiscovery = time.Time{}
		} else {
			if g == nil {
				return "Игра не найдена."
			}
			old := g.Target
			g.Target = target
			if !s.persist() {
				g.Target = old
				return s.notice
			}
		}
		return "Настройки игры сохранены."
	case "stop", "resume", "restart":
		return s.controlGame(action, strings.ToLower(rest))
	case "remove":
		name, confirm := strings.CutPrefix(rest, "confirm ")
		disposition := ""
		if confirm {
			disposition, name, _ = strings.Cut(name, " ")
		}
		return s.requestGameRemoval(strings.ToLower(strings.TrimSpace(name)), disposition, confirm)
	case "exclude", "include":
		game, name, ok := strings.Cut(rest, "|")
		if !ok {
			return "Формат: " + action + " Название игры | Полное название награды"
		}
		return s.changeExclusion(strings.ToLower(strings.TrimSpace(game)), name, action == "exclude")
	case "exclusions":
		return s.exclusionList(strings.ToLower(rest))
	case "game_policy":
		game, values, ok := strings.Cut(rest, "|")
		fields := strings.Fields(values)
		if !ok || len(fields) != 2 {
			return "Формат: Название игры | default, speed или resources, затем запас в минутах (0 = общий)."
		}
		mode := fields[0]
		if mode == "default" {
			mode = ""
		}
		return s.changeGamePolicy(strings.ToLower(strings.TrimSpace(game)), mode, fields[1])
	case "quarantine":
		return s.quarantineSummary(strings.ToLower(rest))
	case "campaign_report":
		g := s.state.Games[strings.ToLower(rest)]
		if g == nil {
			return "Укажите полное название добавленной игры."
		}
		return s.campaignReport(g)
	case "notifications":
		key, value, _ := strings.Cut(rest, " ")
		if rest == "" {
			lines := []string{"Настройки уведомлений"}
			if s.state.NotificationsMuted {
				lines = append(lines, "Общий выключатель: выключено")
			} else {
				lines = append(lines, "Общий выключатель: включено")
			}
			for _, item := range noticeKinds {
				on, ok := s.state.NotificationOverrides[item.Key]
				if !ok {
					on = item.Default
				}
				lines = append(lines, fmt.Sprintf("%s (%s): %t", item.Label, item.Key, on))
			}
			return strings.Join(lines, "\n")
		}
		return s.notificationSetting(key, value)
	case "regroup":
		scope, confirm := strings.CutPrefix(rest, "confirm ")
		if scope != "all" {
			scope = "game:" + scope
		}
		if !confirm {
			return s.requestRegroup(scope, false) + " Команда: regroup confirm " + rest
		}
		return s.requestRegroup(scope, true)
	case "mode":
		if rest != "speed" && rest != "resources" {
			return "Выберите mode speed или mode resources. Сейчас: " + s.state.Settings.modeName() + "."
		}
		old := s.state.Settings.SaveResources
		s.state.Settings.SaveResources = rest == "resources"
		if !s.persist() {
			s.state.Settings.SaveResources = old
			return s.notice
		}
		s.event("mode_changed mode=%q", s.state.Settings.modeName())
		return "Режим: " + s.state.Settings.modeName() + ". Применяется к следующим группам."
	case "limits", "timing":
		old := s.state.Settings
		fields := strings.Fields(rest)
		values := make([]int, len(fields))
		for i, f := range fields {
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 {
				return "Укажите положительные целые числа."
			}
			values[i] = n
		}
		next := old
		if action == "limits" && len(values) == 2 {
			next.MaxGames, next.MaxAccounts = values[0], values[1]
		} else if action == "timing" && len(values) == 3 && values[0] <= 1440 && values[1] <= 100 && values[2] >= 15 && values[2] <= 86400 {
			next.ReserveMinutes, next.ExpectedRatePercent, next.DiscoverySeconds = values[0], values[1], values[2]
		} else {
			return commandHelp("settings")
		}
		s.state.Settings = next
		if !s.persist() {
			s.state.Settings = old
			return s.notice
		}
		return "Настройки сохранены. Активные группы продолжают работу."
	case "miner":
		key, value, _ := strings.Cut(rest, " ")
		if value != "on" && value != "off" {
			return "Укажите on или off."
		}
		options := s.minerOptions()
		switch key {
		case "diagnostics":
			options.Diagnostics = value == "on"
		case "single_log":
			options.SingleLog = value == "on"
		case "pubsub":
			options.UsePubSub = value == "on"
		default:
			return commandHelp("settings")
		}
		return s.saveMinerOptions(options)
	case "assign", "free":
		login, name, _ := strings.Cut(rest, " ")
		key := strings.ToLower(strings.TrimSpace(name))
		if action == "free" {
			key = ""
		} else if s.state.Games[key] == nil || s.state.Games[key].Removing {
			return "Игра не найдена."
		}
		for i := range s.state.Accounts {
			a := &s.state.Accounts[i]
			if !strings.EqualFold(a.Credentials.Login, login) {
				continue
			}
			if s.active[a.Game] != nil || s.active[key] != nil {
				return "Изменение закрепления доступно после остановки майнера игры."
			}
			old := a.Game
			a.Game = key
			if !s.persist() {
				a.Game = old
				return s.notice
			}
			return "Закрепление сохранено."
		}
		return "Аккаунт не найден."
	case "trim":
		return s.trim(rest == "confirm")
	default:
		return commands
	}
}

func (s *scheduler) controlGame(action, key string) string {
	g := s.state.Games[key]
	if g == nil {
		return "Игра не найдена."
	}
	if g.Removing {
		return "Игра удаляется."
	}
	a := s.active[key]
	switch action {
	case "stop":
		if !g.Paused {
			g.Paused = true
			if !s.persist() {
				g.Paused = false
				return s.notice
			}
		}
		s.event("game_paused game=%q", g.Name)
		if a != nil {
			a.stopping = true
			a.process.stop()
			return "Игра на паузе. Майнер останавливается, очередь и состав группы сохранены."
		}
		return "Игра на паузе."
	case "resume":
		if g.Paused {
			g.Paused = false
			if !s.persist() {
				g.Paused = true
				return s.notice
			}
		}
		delete(s.retry, key)
		s.event("game_resumed game=%q", g.Name)
		return "Игра возвращена в очередь. Майнер запустится, когда будет активная кампания и свободный слот."
	case "restart":
		if g.Paused {
			return "Игра на паузе. Сначала возобновите её."
		}
		delete(s.retry, key)
		if a == nil {
			return "Майнер сейчас не запущен. Игра остаётся в очереди."
		}
		a.stopping = true
		a.process.stop()
		s.event("game_restart_requested game=%q", g.Name)
		return "Майнер перезапускается. Состав группы и очередь сохранены."
	}
	return "Действие не распознано."
}

func (s *scheduler) requestGameRemoval(key, disposition string, confirm bool) string {
	g := s.state.Games[key]
	if g == nil {
		return "Игра не найдена."
	}
	if g.Removing {
		return "Удаление игры уже запущено."
	}
	if !confirm {
		return "Удаление уберёт историю кампаний. Для аккаунтов выберите /remove_game confirm release " + g.Name + " или /remove_game confirm delete " + g.Name + "."
	}
	if disposition != "release" && disposition != "delete" {
		return "Укажите release для возврата в свободный пул или delete для удаления аккаунтов."
	}
	g.DeleteAccounts = disposition == "delete"
	if s.active[key] == nil {
		if !s.removeGame(key) {
			g.DeleteAccounts = false
			return s.notice
		}
		if g.DeleteAccounts {
			return "Игра и её аккаунты удалены."
		}
		return "Игра удалена. Её аккаунты возвращены в свободный пул."
	}
	g.Removing = true
	if !s.persist() {
		g.Removing = false
		g.DeleteAccounts = false
		return s.notice
	}
	s.active[key].process.stop()
	if g.DeleteAccounts {
		return "Майнер останавливается. После остановки игра и её аккаунты будут удалены."
	}
	return "Майнер останавливается. После остановки игра будет удалена, а аккаунты вернутся в свободный пул."
}

func (s *scheduler) removeGame(key string) bool {
	g := s.state.Games[key]
	old := s.state.Accounts
	accounts, _ := changePoolMembership(old, key, g.DeleteAccounts)
	delete(s.state.Games, key)
	s.state.Accounts = accounts
	if !s.persist() {
		s.state.Games[key] = g
		s.state.Accounts = old
		return false
	}
	delete(s.retry, key)
	delete(s.airtimes, key)
	return true
}

func (s *scheduler) minerOptions() minerOptions {
	if saved := s.state.Settings.MinerOptions; saved != nil {
		return *saved
	}
	return minerOptions{Diagnostics: s.miner.Diagnostics, UsePubSub: s.miner.UsePubSub}
}

func (s *scheduler) saveMinerOptions(options minerOptions) string {
	old := s.state.Settings.MinerOptions
	s.state.Settings.MinerOptions = &options
	if !s.persist() {
		s.state.Settings.MinerOptions = old
		return s.notice
	}
	return "Настройки майнера сохранены. Они применятся со следующей группы."
}

func (s *scheduler) upload(data []byte) error {
	return s.uploadToPool(data, "")
}

func (s *scheduler) uploadToPool(data []byte, game string) error {
	return s.applyUpload(data, game, true)
}

func (s *scheduler) applyUpload(data []byte, game string, commit bool) error {
	groups, err := parser.Parse(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if game != "" && (s.state.Games[game] == nil || s.state.Games[game].Removing) {
		return fmt.Errorf("игра не найдена")
	}
	byLogin := make(map[string]int, len(s.state.Accounts))
	byID := make(map[string]string, len(s.state.Accounts))
	accounts := append([]account(nil), s.state.Accounts...)
	for i, a := range accounts {
		byLogin[a.Credentials.Login] = i
		byID[a.Credentials.UserID] = a.Credentials.Login
	}
	for _, g := range groups {
		for _, a := range g {
			if login := byID[a.UserID]; login != "" && login != a.Login {
				return fmt.Errorf("Twitch user ID уже закреплён за %s", login)
			}
			if i, ok := byLogin[a.Login]; ok {
				if accounts[i].Credentials.UserID != a.UserID {
					return fmt.Errorf("у %s другой Twitch user ID", a.Login)
				}
				if game != "" {
					if previous := accounts[i].Game; previous != "" && previous != game {
						return fmt.Errorf("аккаунт %s уже закреплён за другой игрой", a.Login)
					}
					accounts[i].Game = game
				}
				if accounts[i].Credentials.OAuth != a.OAuth {
					accounts[i].Quarantined = false

				}
				accounts[i].Credentials = *a
			} else {
				accounts = append(accounts, account{Credentials: *a, Game: game})
				byID[a.UserID] = a.Login
			}
		}
	}
	if !commit {
		return nil
	}
	old := s.state.Accounts
	s.state.Accounts = accounts
	if !s.persist() {
		s.state.Accounts = old
		return fmt.Errorf("не удалось сохранить аккаунты")
	}
	s.lastDiscovery = time.Time{}
	return nil
}

func (s *scheduler) accountErrors(key string) string {
	g := s.state.Games[key]
	if g == nil {
		return "Укажите название игры."
	}
	var current *campaign
	for _, c := range g.Campaigns {
		if c.ID == g.Current {
			current = c
			break
		}
		if current == nil || c.Starts.After(current.Starts) {
			current = c
		}
	}
	if current == nil {
		return "Кампании пока не обнаружены."
	}
	var entries []string
	for login, outcome := range current.Results {
		switch outcome {
		case "error: auth":
			entries = append(entries, login+" · ошибка авторизации")
		case "trimmed":
			entries = append(entries, login+" · остановлен вручную")
		case "stopped":
			entries = append(entries, login+" · остановлен по дедлайну")
		}
	}
	if len(entries) == 0 {
		return "У кампании «" + current.Name + "» нет пропущенных аккаунтов."
	}
	sort.Strings(entries)
	return "Кампания «" + current.Name + "»\n" + strings.Join(entries, "\n")
}

func (cfg settings) modeName() string {
	if cfg.SaveResources {
		return "Экономия ресурсов"
	}
	return "Скорость"
}

func (s *scheduler) groupSummary(key string, c *campaign, now time.Time) string {
	a := s.active[key]
	active := a != nil && !a.groupStarted.IsZero()
	pendingNames := pending(s.state, key, c)
	waiting := len(pendingNames)
	duration := s.duration(s.state.Games[key], c)
	start := now
	running := s.runningAccounts()
	prefix := ""
	if active {
		remainingActive := len(c.Group)
		groupNames := make(map[string]bool, len(c.Group))
		for _, login := range c.Group {
			groupNames[login] = true
		}
		for _, login := range pendingNames {
			if groupNames[login] {
				waiting--
			}
		}
		for _, p := range a.progress {
			if terminal(p.Status) {
				remainingActive--
			}
		}
		running -= remainingActive
		start = now.Add(max(0, a.groupStarted.Add(duration).Sub(now)))
		prefix = fmt.Sprintf("Активная группа: %d\nВ очереди: %d", len(c.Group), waiting)
	}
	if waiting <= 0 {
		if active {
			return prefix
		}
		return "Ожидающих аккаунтов нет."
	}
	if !start.Before(c.Ends) {
		return strings.TrimSpace(prefix + "\n⚠️ По прогнозу времени на следующую группу нет.")
	}
	deadline := s.planDeadline(s.state.Games[key], c)
	count := planGroup(waiting, running, s.state.Settings.MaxAccounts, deadline.Sub(start), duration, s.gameSettings(s.state.Games[key]).SaveResources)
	if active {
		risk := ""
		if deadline.Sub(start) < duration {
			risk = "\n⚠️ До дедлайна может не хватить времени на следующую группу."
		}
		return fmt.Sprintf("%s\nСледующая группа: около %d%s", prefix, count, risk)
	}
	return fmt.Sprintf("В очереди: %d\nПервая группа: около %d", waiting, count)
}

func (s *scheduler) status() string {
	var out strings.Builder
	cfg := s.state.Settings
	free := 0
	pools := make(map[string]int)
	for _, a := range s.state.Accounts {
		if a.Game == "" {
			free++
		} else {
			pools[a.Game]++
		}
	}
	fmt.Fprintf(&out, "Игры: %d/%d. Аккаунты в работе: %d/%d. Свободный пул: %d.\n", len(s.active), cfg.MaxGames, s.runningAccounts(), cfg.MaxAccounts, free)
	fmt.Fprintf(&out, "Режим: %s. Запас: %d мин. Стартовая скорость: %d%%. Поиск: каждые %d сек.\n", cfg.modeName(), cfg.ReserveMinutes, cfg.ExpectedRatePercent, cfg.DiscoverySeconds)
	now := time.Now()
	keys := make([]string, 0, len(s.state.Games))
	for k := range s.state.Games {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := s.state.Games[k]
		label := ""
		if g.Regroup {
			label = " · перегруппировка"
		} else if g.Paused {
			label = " · на паузе"
		} else if a := s.active[k]; a != nil && a.stopping {
			label = " · перезапускается"
		}
		fmt.Fprintf(&out, "\n%s · пул %d/%d%s\n", g.Name, pools[k], g.Target, label)
		fmt.Fprintf(&out, "  %s\n", s.queueSummary(k, g, now))
		items := make([]*campaign, 0, len(g.Campaigns))
		for _, c := range g.Campaigns {
			items = append(items, c)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Ends.Before(items[j].Ends) })
		if len(items) == 0 {
			fmt.Fprintln(&out, "  Кампании пока не обнаружены.")
		}
		for _, c := range items {
			status := c.Finished
			if status == "" {
				switch {
				case time.Now().After(c.Ends):
					status = "срок истёк"
				case time.Now().Before(c.Starts):
					status = "ожидает старта"
				case allRewardsExcluded(c):
					status = "все награды исключены"
				case g.Paused:
					status = "на паузе"
				case g.Current == c.ID && s.active[k] != nil:
					if s.active[k].stopping {
						status = "перезапускается"
					} else {
						status = "в работе"
					}
				default:
					status = "в очереди"
				}
			}
			ready, skipped := 0, 0
			for _, r := range c.Results {
				if r == "idle" {
					ready++
				} else {
					skipped++
				}
			}
			fmt.Fprintf(&out, "  %s · %s · готово %d, пропущено %d · до %s\n", c.Name, status, ready, skipped, c.Ends.Local().Format("02.01 15:04 MST"))

		}
	}
	fmt.Fprintln(&out, s.catalogSummary())
	if s.notice != "" {
		fmt.Fprintln(&out, s.notice)
	}
	return out.String()
}

type trimAccount struct {
	game, login string
	group       time.Time
	position    int
	eta         time.Duration
}

func (s *scheduler) trim(confirm bool) string {
	extra := s.runningAccounts() - s.state.Settings.MaxAccounts
	if extra <= 0 {
		return "Нагрузка в пределах лимита."
	}
	var choices []trimAccount
	for key, a := range s.active {
		if a.removing {
			return "Предыдущее снижение нагрузки ещё выполняется."
		}
		g := s.state.Games[key]
		c := g.Campaigns[g.Current]
		if c == nil || a.groupStarted.IsZero() {
			continue
		}
		progress := make(map[string]struct {
			status string
			eta    time.Duration
		})
		for _, p := range a.progress {
			rate := float64(s.state.Settings.ExpectedRatePercent) / 100
			if p.RateKnown {
				rate = max(0.05, p.Rate)
			}
			minutes := max(0, p.Required-p.Current) + p.RemainingAfterCurrent
			if p.Required == 0 {
				minutes = c.Minutes
			}
			progress[p.Login] = struct {
				status string
				eta    time.Duration
			}{p.Status, estimate(minutes, rate)}
		}
		for position, name := range c.Group {
			p, known := progress[name]
			if terminal(p.status) {
				continue
			}
			if !known {
				p.eta = s.duration(g, c)
			}
			choices = append(choices, trimAccount{game: key, login: name, group: a.groupStarted, position: position, eta: p.eta})
		}
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].eta != choices[j].eta {
			return choices[i].eta > choices[j].eta
		}
		if choices[i].game == choices[j].game {
			return choices[i].position > choices[j].position
		}
		return choices[i].game < choices[j].game
	})
	choices = choices[:min(extra, len(choices))]
	names := make([]string, len(choices))
	for i, c := range choices {
		names[i] = c.login + "@" + c.group.Format(time.RFC3339Nano)
	}
	if !confirm || time.Now().After(s.trimUntil) || !sameNames(names, s.trimPreview) {
		s.trimPreview = names
		s.trimUntil = time.Now().Add(2 * time.Minute)
		return fmt.Sprintf("Будет остановлено %d отстающих аккаунтов. Целевая нагрузка %d. Для подтверждения: /reduce_load confirm.", len(names), s.runningAccounts()-len(names))
	}
	byGame := make(map[string][]string)
	for _, c := range choices {
		byGame[c.game] = append(byGame[c.game], c.login)
	}
	for key, names := range byGame {
		g := s.state.Games[key]
		c := g.Campaigns[g.Current]
		if c.Results == nil {
			c.Results = make(map[string]string)
		}
		for _, name := range names {
			c.Results[name] = "trimmed"
		}
	}
	if !s.persist() {
		for key, names := range byGame {
			g := s.state.Games[key]
			for _, name := range names {
				delete(g.Campaigns[g.Current].Results, name)
			}
		}
		return s.notice
	}
	for key, names := range byGame {
		a := s.active[key]
		a.process.commands <- minerCommand{Remove: names}
		a.removing = true
	}
	s.trimPreview = nil
	return fmt.Sprintf("Отправлена команда остановки %d отстающих аккаунтов.", len(names))
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
