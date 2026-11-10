package orchestrator

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

type activeGame struct {
	streams         []twitch.Stream
	authNotified    map[string]bool
	stallNotified   bool
	logDir          string
	process         *minerProcess
	plannedDuration time.Duration
	groupStarted    time.Time
	rateChecked     time.Time
	progress        []worker.ManagedProgress
	removing        bool
	stopping        bool
	discardResult   bool
}

type scheduler struct {
	airtimes         map[string]*campaignAirtime
	airtimeChecks    int
	statsDirty       bool
	telegramSearch   telegramSearchResult
	telegramCoverage campaignExportSelection
	journal          *log.Logger
	categoryNames    []string
	botChanges       chan struct{}
	mu               sync.Mutex
	path             string
	state            *state
	miner            config.Config
	active           map[string]*activeGame
	updates          chan processUpdate
	catalogs         chan discoveryResult
	retry            map[string]time.Time
	lastDiscovery    time.Time
	discovering      bool
	discoveryError   string
	notice           string
	notify           func(string)
	trimPreview      []string
	trimUntil        time.Time
}

type discoveryResult struct {
	campaigns []twitch.Campaign
	err       error
}

func (s *scheduler) discover(ctx context.Context, now time.Time) {
	if s.discovering || now.Sub(s.lastDiscovery) < time.Duration(s.state.Settings.DiscoverySeconds)*time.Second {
		return
	}
	s.lastDiscovery = now
	var accounts []account
	for _, a := range s.state.Accounts {
		if !a.Quarantined {
			accounts = append(accounts, a)
		}
	}
	if len(accounts) == 0 {
		s.discoveryError = "нет доступного аккаунта для поиска кампаний"
		return
	}
	s.discovering = true
	cfg := s.miner
	go func() {
		result := discoveryResult{}
		client := &http.Client{Timeout: 15 * time.Second}
		defer client.CloseIdleConnections()
		// Rotate the first choice so a run of invalid credentials cannot hide good accounts.
		start := int((now.Unix() / int64(15)) % int64(len(accounts)))
		for i := 0; i < min(5, len(accounts)); i++ {
			a := accounts[(start+i)%len(accounts)].Credentials
			result.campaigns, result.err = twitch.NewGQLClient(client, &cfg, &a).Campaigns(ctx)
			if result.err == nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case s.catalogs <- result:
		case <-ctx.Done():
		}
	}()
}

func (s *scheduler) applyCatalog(result discoveryResult, now time.Time) {
	s.discovering = false
	if result.err != nil {
		log.Printf("catalog discovery: %v", result.err)
		s.discoveryError = "Twitch не вернул каталог. Поиск повторится автоматически."
		s.catalogFailed()
		return
	}
	s.catalogSucceeded(now)
	s.discoveryError = ""
	s.categoryNames = nil
	seen := make(map[string]bool)
	for _, remote := range result.campaigns {
		if name := remote.Game.Name; name != "" && !seen[name] {
			s.categoryNames = append(s.categoryNames, name)
			seen[name] = true
		}
		g := s.state.Games[strings.ToLower(remote.Game.Name)]
		if g == nil || remote.ID == "" {
			continue
		}
		var rewards []rewardRequirement
		for _, drop := range remote.TimeBasedDrops {
			if drop.Required > 0 {
				rewards = append(rewards, rewardRequirement{ID: drop.ID, Name: drop.Name, Minutes: drop.Required})
			}
		}
		if len(rewards) == 0 {
			continue
		}
		if remote.EndAt.IsZero() || remote.StartAt.IsZero() {
			s.discoveryError = "У кампании «" + remote.Name + "» отсутствуют сроки. Поиск повторится автоматически."
			continue
		}
		if g.Campaigns == nil {
			g.Campaigns = make(map[string]*campaign)
		}
		c := g.Campaigns[remote.ID]
		if c == nil {
			if !now.Before(remote.EndAt) {
				continue
			}
			c = &campaign{ID: remote.ID, Discovered: now, Results: make(map[string]string)}
			g.Campaigns[c.ID] = c
			s.sendNotice("campaign_new", fmt.Sprintf("«%s»: новая кампания «%s», до %s.", g.Name, remote.Name, remote.EndAt.Local().Format("02.01 15:04 MST")))
		}
		c.Name, c.Status, c.Starts, c.Ends, c.Minutes = remote.Name, remote.Status, remote.StartAt, remote.EndAt, requiredMinutes(rewards, s.configForGame(g))
		c.Rewards = rewards
	}
	s.persist()
}

func (s *scheduler) persist() bool {
	if err := saveState(s.path, s.state); err != nil {
		log.Printf("save orchestrator state: %v", err)
		s.notice = "Ошибка записи состояния оркестратора."
		return false
	}
	s.statsDirty = false
	return true
}

func (s *scheduler) warn(message string) {
	s.notice = message
	log.Print(message)
	s.sendNotice("capacity", message)
}

func estimate(minutes int, rate float64) time.Duration {
	return time.Duration(math.Ceil(float64(minutes)/rate)) * time.Minute
}

func (s *scheduler) duration(g *gameState, c *campaign) time.Duration {
	minutes := c.Minutes
	if len(c.Rewards) > 0 {
		minutes = requiredMinutes(c.Rewards, s.configForGame(g))
	}
	return max(time.Minute, estimate(minutes, s.planningRate(c)))
}

func (s *scheduler) planDeadline(g *gameState, c *campaign) time.Time {
	return c.Ends.Add(-time.Duration(s.gameSettings(g).ReserveMinutes) * time.Minute)
}

func (s *scheduler) planningRate(c *campaign) float64 {
	settings := s.state.Settings
	rate := float64(settings.ExpectedRatePercent) / 100
	if c.ObservedRate > 0 {
		rate = min(rate, c.ObservedRate)
	}
	return rate
}

func planGroup(pending, active, limit int, left, duration time.Duration, saveResources bool) int {
	waves := max(1, int(left/duration))
	minimum := (pending-1)/waves + 1
	free := max(0, limit-active)
	// Economy uses all predicted waves. If capacity is below the minimum,
	// either wait for capacity or exceed the limit as the deadline requires.
	if saveResources || free < minimum {
		return minimum
	}
	wavesNeeded := (pending-1)/free + 1
	return (pending-1)/wavesNeeded + 1
}

func allRewardsExcluded(c *campaign) bool {
	return len(c.Rewards) > 0 && c.Minutes == 0
}

func campaignReady(c *campaign, now time.Time) bool {
	return c.Finished == "" && !allRewardsExcluded(c) && !now.Before(c.Starts) && now.Before(c.Ends) &&
		(strings.EqualFold(c.Status, "ACTIVE") || c.Status == "")
}

func hasUpcoming(g *gameState, now time.Time) bool {
	for _, c := range g.Campaigns {
		if c.Finished == "" && now.Before(c.Ends) {
			return true
		}
	}
	return false
}

func (s *scheduler) nextCampaign(g *gameState, now time.Time) *campaign {
	var best *campaign
	for _, c := range g.Campaigns {
		if !campaignReady(c, now) {
			continue
		}
		if best == nil || s.less(g, c, g, best) {
			best = c
		}
	}
	return best
}

func (s *scheduler) latestStart(g *gameState, c *campaign, target int) time.Time {
	waves := max(1, (target-1)/s.state.Settings.MaxAccounts+1)
	// Saturate before multiplying a duration by an administrator-provided target.
	duration := s.duration(g, c)
	if duration > 0 && time.Duration(waves) > time.Duration(1<<63-1)/duration {
		return time.Time{}
	}
	return s.planDeadline(g, c).Add(-time.Duration(waves) * duration)
}

func (s *scheduler) less(gameA *gameState, a *campaign, gameB *gameState, b *campaign) bool {
	first, second := s.latestStart(gameA, a, gameA.Target), s.latestStart(gameB, b, gameB.Target)
	if !first.Equal(second) {
		return first.Before(second)
	}
	if !a.Discovered.Equal(b.Discovered) {
		return a.Discovered.Before(b.Discovered)
	}
	return a.ID < b.ID
}

func (s *scheduler) runningAccounts() int {
	total := 0
	for key, a := range s.active {
		if c := s.state.Games[key].Campaigns[s.state.Games[key].Current]; c != nil && !a.groupStarted.IsZero() {
			total += len(c.Group)
			for _, p := range a.progress {
				if terminal(p.Status) {
					total--
				}
			}
		}
	}
	return total
}

func finishCampaign(c *campaign) {
	c.Finished = "завершена"
	for _, outcome := range c.Results {
		if outcome != "idle" {
			c.Finished = "завершена с пропусками"
			return
		}
	}
}

func terminal(status string) bool {
	return status == "idle" || status == "error: auth" || status == "stopped"
}

// Wait for capacity only when the full normal-limit schedule still fits.
func (s *scheduler) canWaitForCapacity(deadline time.Time, count int, duration time.Duration, now time.Time) bool {
	limit := s.state.Settings.MaxAccounts
	waves := (count-1)/limit + 1
	release := now
	hasRunning := false
	for key, active := range s.active {
		if active.groupStarted.IsZero() {
			continue
		}
		g := s.state.Games[key]
		other := g.Campaigns[g.Current]
		if other == nil {
			continue
		}
		hasRunning = true
		expected := active.groupStarted.Add(s.duration(g, other))
		if !expected.After(now) {
			// Once an estimate has passed, use the observed remaining work.
			expected = now.Add(s.duration(g, other))
		}
		if other.Ends.Before(expected) {
			expected = other.Ends
		}
		if expected.After(release) {
			release = expected
		}
	}
	return hasRunning && deadline.Sub(release)/duration >= time.Duration(waves)
}

func (s *scheduler) launchGroup(key string, g *gameState, c *campaign, a *activeGame, now time.Time) {
	if !a.groupStarted.IsZero() {
		return
	}
	restored := len(c.Group) > 0
	names := pending(s.state, key, c)
	duration := s.duration(g, c)
	if !restored {
		if len(names) == 0 {
			if len(c.Results) > 0 {
				finishCampaign(c)
				s.state.Stats.recordCampaign(key, c, now.Local().Format(statsDayLayout))
				s.persist()
			}
			return
		}

		running, limit := s.runningAccounts(), s.state.Settings.MaxAccounts
		deadline := s.planDeadline(g, c)
		count := planGroup(len(names), running, limit, deadline.Sub(now), duration, s.gameSettings(g).SaveResources)
		if count > max(0, limit-running) && s.canWaitForCapacity(deadline, len(names), duration, now) {
			return
		}
		c.Group = append([]string(nil), names[:count]...)
	}
	wanted := make(map[string]bool, len(c.Group))
	for _, name := range c.Group {
		wanted[name] = true
	}
	var accounts []*parser.Account
	for i := range s.state.Accounts {
		acc := &s.state.Accounts[i]
		if acc.Game == key && !acc.Quarantined && wanted[acc.Credentials.Login] && c.Results[acc.Credentials.Login] == "" {
			credentials := acc.Credentials
			accounts = append(accounts, &credentials)
		}
	}
	if len(accounts) == 0 {
		c.Group = nil
		return
	}
	c.Group = c.Group[:0]
	for _, acc := range accounts {
		c.Group = append(c.Group, acc.Login)
	}
	if !s.persist() {
		return
	}
	cfg := s.configForGame(g)
	cfg.TargetGame, cfg.CampaignID = g.Name, c.ID
	cfg.LogDir = a.logDir
	options := s.minerOptions()
	cfg.Diagnostics, cfg.SingleLog, cfg.UsePubSub = options.Diagnostics, options.SingleLog, options.UsePubSub
	cfg.TelegramBotToken, cfg.DiscordBotToken = "", ""
	a.streams = s.airtimes[key].streams
	a.process.commands <- minerCommand{Config: &cfg, Accounts: accounts, Deadline: c.Ends, Streams: a.streams}
	a.groupStarted = now
	a.plannedDuration = duration
	a.authNotified = nil
	a.stallNotified = false
	s.sendNotice("group_start", fmt.Sprintf("«%s» · %s: старт группы из %d аккаунтов.", g.Name, c.Name, len(accounts)))
	a.rateChecked = time.Time{}
	total := s.runningAccounts()
	s.event("group_started game=%q campaign=%q mode=%q restored=%t group=%d pending_before=%d time_left=%s rate=%.3f reserve_min=%d duration=%s predicted_waves=%d active_total=%d limit=%d log_dir=%q", g.Name, c.Name, s.gameSettings(g).modeName(), restored, len(accounts), len(names), c.Ends.Sub(now).Round(time.Second), s.planningRate(c), s.gameSettings(g).ReserveMinutes, duration, max(0, int(s.planDeadline(g, c).Sub(now)/duration)), total, s.state.Settings.MaxAccounts, filepath.Base(a.logDir))
	if total > s.state.Settings.MaxAccounts {
		s.warn(fmt.Sprintf("«%s»: для дедлайна запущена группа из %d. Общая нагрузка %d при лимите %d. Команда: /reduce_load", g.Name, len(accounts), total, s.state.Settings.MaxAccounts))
	}
}

func (s *scheduler) tick(ctx context.Context, now time.Time) {
	s.discover(ctx, now)
	s.finishRegroups()
	for key, g := range s.state.Games {
		if !g.Removing {
			continue
		}
		if a := s.active[key]; a != nil {
			a.process.stop()
		} else {
			s.removeGame(key)
		}
	}
	for key, a := range s.active {
		g := s.state.Games[key]
		if g.Removing || g.Paused || g.Regroup || a.stopping {
			continue
		}
		c := g.Campaigns[g.Current]
		if c == nil {
			a.process.stop()
			continue
		}
		if !now.Before(c.Ends) {
			// Wait for the final snapshot, which retains confirmed completions.
			if !a.groupStarted.IsZero() {
				continue
			}
			c.Finished = "срок истёк"
		}
		if c.Finished != "" || allRewardsExcluded(c) {
			next := s.nextCampaign(g, now)
			if next == nil {
				g.SeriesPaused = hasUpcoming(g, now)
				g.Current = ""
				s.persist()
				a.process.stop()
				continue
			}
			g.Current = next.ID
			c = next
		}
		air := s.checkCampaignAirtime(ctx, key, g, c, now)
		if len(air.streams) == 0 {
			if !a.groupStarted.IsZero() && air.failures == 1 {
				continue // Keep the confirmed set until the next poll after one inconclusive result.
			}
			a.stopping = true
			s.event("group_waiting_for_airtime game=%q campaign=%q reason=%q", g.Name, c.Name, air.status())
			a.process.stop()
			continue
		}
		if !a.groupStarted.IsZero() && !air.sameStreams(a.streams) {
			select {
			case a.process.commands <- minerCommand{UpdateStreams: true, Streams: air.streams}:
				a.streams = air.streams
				s.event("group_streams_updated game=%q campaign=%q streams=%d", g.Name, c.Name, len(a.streams))
			default:
				// Retry with the latest list once the miner can receive a command.
			}
		}
		s.launchGroup(key, g, c, a, now)
	}
	type candidate struct {
		key          string
		campaign     *campaign
		continuation bool
	}
	var candidates []candidate
	for key, g := range s.state.Games {
		if g.Removing || g.Paused || g.Regroup || s.active[key] != nil || now.Before(s.retry[key]) {
			continue
		}
		c := g.Campaigns[g.Current]
		if c == nil || !campaignReady(c, now) {
			c = s.nextCampaign(g, now)
		}
		if c != nil {
			candidates = append(candidates, candidate{key, c, g.SeriesPaused || g.Current != ""})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.continuation != b.continuation {
			return a.continuation
		}
		return s.less(s.state.Games[a.key], a.campaign, s.state.Games[b.key], b.campaign)
	})
	for _, item := range candidates {
		g, c := s.state.Games[item.key], item.campaign
		if assignPool(s.state, item.key) {
			s.persist()
		}
		if len(pending(s.state, item.key, c)) == 0 {
			continue
		}
		if len(s.checkCampaignAirtime(ctx, item.key, g, c, now).streams) == 0 || len(s.active) >= s.state.Settings.MaxGames {
			continue
		}
		p, err := startMiner(ctx, item.key, s.updates)
		if err != nil {
			log.Printf("start miner: %v", err)
			s.retry[item.key] = now.Add(time.Minute)
			continue
		}
		a := &activeGame{process: p, logDir: filepath.Join(filepath.Dir(s.path), "logs", "game-"+gameRef(item.key))}
		s.active[item.key] = a
		g.Current, g.SeriesPaused = c.ID, false
		s.launchGroup(item.key, g, c, a, now)
	}
}

func (s *scheduler) update(u processUpdate, now time.Time) {
	a := s.active[u.game]
	if a == nil || a.process != u.process {
		return
	}
	if u.exited {
		delete(s.active, u.game)
		s.event("slot_released game=%q active_games=%d", u.game, len(s.active))
		if !a.stopping && !a.groupStarted.IsZero() {
			s.retry[u.game] = now.Add(15 * time.Second)
		}
		return
	}
	g := s.state.Games[u.game]
	c := g.Campaigns[g.Current]
	if c == nil {
		return
	}
	if s.state.Stats.recordProgress(u.game, c.ID, u.event.Progress, now) {
		s.statsDirty = true
	}
	if a.discardResult {
		if s.statsDirty {
			s.persist()
		}
		return
	}
	a.progress = u.event.Progress
	if !a.stopping {
		s.observeGroupNotices(a, g, a.progress)
	}
	for _, message := range u.event.Rewards {
		s.sendNotice("reward", g.Name+" · "+message)
	}
	updateRate(c, a, a.progress, float64(s.state.Settings.ExpectedRatePercent)/100)
	if !u.event.Done {
		if s.statsDirty {
			s.persist()
		}
		return
	}
	if c.Results == nil {
		c.Results = make(map[string]string)
	}
	for _, p := range a.progress {
		if a.stopping && p.Status == "stopped" {
			continue
		}
		if terminal(p.Status) && (c.Results[p.Login] == "" || p.Status == "idle") {
			c.Results[p.Login] = p.Status
		}
	}
	if a.stopping && len(pending(s.state, u.game, c)) > 0 {
		s.persist()
		return
	}
	if !a.stopping {
		if !a.groupStarted.IsZero() {
			c.CompletedGroups++
			s.state.Stats.credit(now.Local().Format(statsDayLayout), u.game, totals{Groups: 1})
			c.PlannedGroupTime += a.plannedDuration
			c.ActualGroupTime += max(0, now.Sub(a.groupStarted))
		}
		ready := 0
		for _, p := range a.progress {
			if p.Status == "idle" {
				ready++
			}
		}
		s.sendNotice("group_done", fmt.Sprintf("«%s» · %s: группа завершила работу, готово %d из %d аккаунтов.", g.Name, c.Name, ready, len(c.Group)))
	}
	c.Group = nil
	a.groupStarted = time.Time{}
	a.progress = nil
	a.removing = false
	if !now.Before(c.Ends) {
		c.Finished = "срок истёк"
	} else if len(pending(s.state, u.game, c)) == 0 {
		finishCampaign(c)
		s.state.Stats.recordCampaign(u.game, c, now.Local().Format(statsDayLayout))
	}
	if s.persist() && c.Finished != "" {
		ready := 0
		for _, result := range c.Results {
			if result == "idle" {
				ready++
			}
		}
		s.sendNotice("campaign_done", fmt.Sprintf("«%s» · %s: %s. Награды готовы у %d аккаунтов.", g.Name, c.Name, c.Finished, ready))
	}
}
