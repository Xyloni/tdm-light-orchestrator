package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

// accountWorker — горутина одного аккаунта внутри активной группы.
type accountWorker struct {
	m                *Manager
	acc              *parser.Account
	st               *AccountState
	gql              *twitch.GQLClient
	startupDelay     time.Duration
	unavailable      map[string]time.Time
	nextRateRecovery time.Time
}

func (w *accountWorker) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	pubsubDone := make(chan struct{})
	defer func() {
		cancel()
		<-pubsubDone
	}()
	defer func() {
		if ctx.Err() != nil && !w.st.authFailed.Load() {
			w.st.setStatus("stopped")
		}
	}()
	rnd := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w.acc.Line)*7919))

	// Задержка рассчитана менеджером по числу аккаунтов активной группы.
	if !sleepCtx(ctx, w.startupDelay) {
		close(pubsubDone)
		return
	}

	// Опциональный WebSocket-канал событий дропов (по умолчанию выключен).
	if w.m.cfg.UsePubSub {
		go func() {
			defer close(pubsubDone)
			w.gql.PubSubLoop(ctx, func(topic string, msg json.RawMessage) {
				s := string(msg)
				if len(s) > 200 {
					s = s[:200] + "…"
				}
				w.st.addLog("pubsub: " + s)
			})
		}()
	} else {
		close(pubsubDone)
	}

	errCount, authErrors := 0, 0
	for ctx.Err() == nil {
		drop, ok, err := w.nextDrop(ctx)
		if err != nil {
			errCount++
			w.m.cycleErrors.Store(true)
			if isAuthError(err) {
				authErrors++
				if w.retryAuth(ctx, rnd, authErrors, err) {
					continue
				}
				return
			}
			w.st.addLog("dashboard: " + err.Error())
			if !sleepCtx(ctx, backoff(errCount, rnd)) {
				return
			}
			continue
		}
		errCount = 0
		if !ok {
			w.st.setStatus("idle")
			w.st.addLog("нет активных дропов по «" + w.m.cfg.TargetGame + "», сессия завершена")
			return
		}
		w.m.cycleWorked.Store(true)
		if err := w.farmDrop(ctx, rnd, drop); err != nil && ctx.Err() == nil {
			w.m.cycleErrors.Store(true)
			if w.m.cfg.ManagedAuthAttempts > 0 && isAuthError(err) {
				authErrors++
				if w.retryAuth(ctx, rnd, authErrors, err) {
					continue
				}
				return
			}
			authErrors = 0
			w.st.addLog("фарм: " + err.Error())
			if !sleepCtx(ctx, jittered(rnd, 30*time.Second, w.m.cfg.JitterSec)) {
				return
			}
		} else {
			authErrors = 0
		}
	}
	w.st.setStatus("stopped")
}

func (w *accountWorker) retryAuth(ctx context.Context, rnd *rand.Rand, attempts int, err error) bool {
	if attempts < w.m.cfg.ManagedAuthAttempts {
		return sleepCtx(ctx, backoff(attempts, rnd))
	}
	w.st.authFailed.Store(true)
	w.st.setStatus("error: auth")
	w.st.addLog("ошибка авторизации, воркер остановлен: " + err.Error())
	return false
}

// nextDrop — первый невыкупленный и незавершённый дроп по целевой игре.
func (w *accountWorker) nextDrop(ctx context.Context) (twitch.Drop, bool, error) {
	drops, err := w.gql.DropsDashboard(ctx)
	if err != nil {
		return twitch.Drop{}, false, err
	}
	matched := false
	var selected twitch.Drop
	remainingAfterCurrent := 0
	for _, d := range drops {
		if w.m.cfg.CampaignID != "" && d.CampaignID != w.m.cfg.CampaignID {
			continue
		}
		if !strings.EqualFold(d.GameName, w.m.cfg.TargetGame) {
			continue
		}
		if d.CampaignStatus != "" && !strings.EqualFold(d.CampaignStatus, "ACTIVE") {
			continue
		}
		matched = true
		if w.m.cfg.DropIgnored(d.Name) {
			continue
		}
		if d.IsClaimed {
			w.markReady(d)
			continue
		}
		if d.Required <= 0 {
			continue
		}
		if d.ProgressKnown && d.Current >= d.Required {
			w.st.setProgress(d)
			w.markReady(d)
			continue
		}
		if w.m.cfg.ManagedAuthAttempts == 0 {
			w.st.mu.Lock()
			w.st.observePendingLocked(d)
			w.st.mu.Unlock()
			return d, true, nil
		}
		if selected.ID == "" {
			selected = d
		} else if d.ProgressKnown {
			remainingAfterCurrent += max(0, d.Required-d.Current)
		} else {
			remainingAfterCurrent += d.Required
		}
	}
	if selected.ID != "" {
		w.st.setProgress(selected)
		w.st.mu.Lock()
		w.st.remainingAfterCurrent = remainingAfterCurrent
		w.st.mu.Unlock()
		return selected, true, nil
	}
	if w.m.cfg.ManagedAuthAttempts > 0 && !matched {
		return twitch.Drop{}, false, fmt.Errorf("кампания %s пока недоступна аккаунту", w.m.cfg.CampaignID)
	}
	return twitch.Drop{}, false, nil
}

// farmDrop набирает время дропа. Получение награды остаётся пользователю.
func (w *accountWorker) farmDrop(ctx context.Context, rnd *rand.Rand, drop twitch.Drop) error {
	return w.farmDropScheduled(ctx, rnd, drop, watchIntervals{
		hls:      20 * time.Second,
		beacon:   time.Duration(maxInt(w.m.cfg.HeartbeatIntervalSec, 60)) * time.Second,
		progress: time.Duration(maxInt(w.m.cfg.CampaignPollIntervalSec, 60)) * time.Second,
	})
}

type watchIntervals struct {
	hls, beacon, progress time.Duration
	timeouts              operationTimeouts
}

// HLS/recovery stays owned by this worker; beacon and Inventory each have one
// sequential background loop. No request goroutines or pending jobs accumulate.
func (w *accountWorker) farmDropScheduled(parent context.Context, rnd *rand.Rand, drop twitch.Drop, intervals watchIntervals) error {
	ctx, cancel := context.WithCancel(parent)
	budgets := intervals.timeouts.defaults()
	w.st.clearWatchRate()
	defer w.st.clearWatchRate()
	w.st.setStatus("farm: " + drop.CampaignName)
	w.st.addLog(fmt.Sprintf("фарм дропа «%s» начат (цель %d мин)", drop.Name, drop.Required))
	w.reportProgress(drop)

	streams := newWatchStream(ctx)
	progress := make(chan inventoryResult, 1)
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); w.beaconLoop(ctx, streams, intervals.beacon, budgets.beacon) }()
	go func() {
		defer background.Done()
		w.inventoryLoop(ctx, drop.ID, intervals.progress, budgets.inventory, progress)
	}()
	defer func() { cancel(); streams.clear(); background.Wait() }()

	variant := ""
	var stream twitch.Stream
	nextHLS := time.Time{}
	nextOpen := time.Time{}
	openFailures := 0
	transientFailures := 0
	hlsRetry := false
	var retryQueuedAt time.Time
	var overall watchRate
	overall.reset(time.Now(), drop.Current, drop.ProgressKnown)
	var rate, sustained watchRate
	var rateStarted time.Time
	handleProgress := func(result inventoryResult) bool {
		if result.err != nil {
			w.st.addLog("прогресс: " + result.err.Error())
			return false
		}
		cur := result.drop
		if !result.found {
			w.st.addLog("дроп пропал из дашборда")
			return true
		}
		if cur.IsClaimed {
			w.st.addLog("«" + cur.Name + "» уже получен")
			return true
		}
		w.reportProgress(cur)
		overall.observe(result.checked, cur.Current, cur.ProgressKnown, 2*intervals.progress+time.Minute)
		w.st.setWatchRate(&overall)
		drop = cur
		if cur.ProgressKnown && cur.Current >= cur.Required {
			w.st.observeRateRecovery(cur.ID, true, nil)
			w.markReady(cur)
			return true
		}
		// A poll that began before the current playback observation window
		// cannot measure that window. Still retain its confirmed progress above.
		if variant == "" || result.started.Before(rateStarted) {
			return false
		}
		slow, gain, elapsed := rate.observe(result.checked, cur.Current, cur.ProgressKnown, 2*intervals.progress+time.Minute)
		w.st.observeRateRecovery(cur.ID, false, &rate)
		longSlow, longGain, longElapsed := sustained.observeSustained(result.checked, cur.Current, cur.ProgressKnown, 2*intervals.progress+time.Minute, gain, elapsed)
		reason := "короткое окно"
		if !slow && longSlow {
			reason = "длительное окно"
			slow, gain, elapsed = true, longGain, longElapsed
		}
		// Overall status includes playback outages and channel switches.
		if slow && w.reserveRateRecovery(result.checked, rnd) {
			w.st.beginRateRecovery(cur.ID, result.checked)
			w.st.addLog(fmt.Sprintf("медленное начисление на %s: +%d мин за %.1f мин (%s); восстанавливаю просмотр", stream.Login, gain, elapsed.Minutes(), reason))
			recoveryCtx, stop := context.WithTimeout(ctx, budgets.recovery)
			selected, playlist, err := w.recoverSlowStream(recoveryCtx, rnd, stream)
			stop()
			if ctx.Err() != nil {
				w.st.endRateRecovery(time.Now(), "unmeasured")
				return true
			}
			if err != nil {
				w.st.endRateRecovery(time.Now(), "failed")
				w.st.addLog("восстановление скорости не удалось; продолжаю текущий просмотр")
			} else {
				stream, variant = selected, playlist
				streams.set(stream)
				nextHLS = time.Time{}
			}
			rateStarted = time.Now()
			if err == nil {
				w.st.endRateRecovery(rateStarted, "measuring")
			}
			rate.reset(rateStarted, cur.Current, cur.ProgressKnown)
			sustained.reset(rateStarted, cur.Current, cur.ProgressKnown)
		}
		return false
	}
	waitForWork := func(deadline time.Time) bool {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return true
		case result := <-progress:
			return handleProgress(result)
		case <-timer.C:
			return false
		}
	}
	for ctx.Err() == nil {
		// Consume the latest completed poll before doing more network work.
		select {
		case result := <-progress:
			if handleProgress(result) {
				return nil
			}
		default:
		}
		if ctx.Err() != nil {
			return nil
		}
		if variant == "" {
			if time.Now().Before(nextOpen) {
				if waitForWork(nextOpen) {
					return nil
				}
				continue
			}
			recoveryCtx, stop := context.WithTimeout(ctx, budgets.recovery)
			selected, playlist, err := w.openStream(recoveryCtx, rnd)
			stop()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				if isAuthError(err) {
					return err
				}
				w.st.addLog("поиск канала: " + err.Error())
				openFailures++
				delay := channelRetryDelay(err, openFailures, rnd)
				nextOpen = time.Now().Add(delay)
				w.st.addLog(fmt.Sprintf("следующая попытка выбора канала через %s", delay.Round(time.Second)))
				continue // Inventory continues during the bounded playback retry pause.
			}
			stream, variant = selected, playlist
			rateStarted = time.Now()
			rate.reset(rateStarted, drop.Current, drop.ProgressKnown)
			sustained.reset(rateStarted, drop.Current, drop.ProgressKnown)
			streams.set(stream)
			nextHLS = time.Time{}
			w.st.addLog("просмотр канала: " + stream.Login)
		}
		if !time.Now().Before(nextHLS) {
			started := time.Now()
			if hlsRetry && !w.m.hlsRetries.tryStart(started) {
				if retryQueuedAt.IsZero() {
					retryQueuedAt = started
				}
				nextHLS = started.Add(time.Second + time.Duration(rnd.Int63n(int64(time.Second))))
				continue
			}
			if !retryQueuedAt.IsZero() {
				if w.m.cfg.Diagnostics {
					w.st.addLog(fmt.Sprintf("diagnostic hls_retry queue_ms=%d", started.Sub(retryQueuedAt).Milliseconds()))
				}
				retryQueuedAt = time.Time{}
				started = time.Now()
			}
			checkCtx, stop := context.WithTimeout(ctx, budgets.hls)
			result, err := w.gql.WatchHLS(checkCtx, variant, stream)
			stop()
			if hlsRetry {
				w.m.hlsRetries.finish()
			}
			if ctx.Err() != nil {
				return nil
			}
			nextHLS = signalDeadline(nextHLS, started, time.Now(), intervals.hls)
			if err != nil {
				if temporaryPlaybackError(err) {
					hlsRetry = true
					w.m.hlsRetries.failed(time.Now())
					transientFailures++
					if transientFailures < 3 {
						delay := retryHLSDuration(intervals.hls, transientFailures, rnd)
						nextHLS = time.Now().Add(delay)
						w.st.addLog(fmt.Sprintf("канал %s: временный сбой HLS (%v; %.1f с), повтор %d/3 через %s; маяк продолжает работу", stream.Login, err, time.Since(started).Seconds(), transientFailures, delay.Round(time.Millisecond)))
						continue
					}
				}
				transientFailures = 0
				w.st.addLog(fmt.Sprintf("канал %s: HLS не восстановлен (%v; %.1f с), переключаю канал", stream.Login, err, time.Since(started).Seconds()))
				duration := w.excludeFailedChannel(stream.Login, err, rnd)
				w.st.addLog(fmt.Sprintf("канал %s: пауза %s", stream.Login, duration))
				streams.clear()
				variant = ""
				continue
			}
			transientFailures = 0
			hlsRetry = false
			if result.VariantURL != "" {
				variant = result.VariantURL
			}
			openFailures = 0
			if result.Warning != "" {
				w.st.addLog("HLS предупреждение: " + result.Warning)
			}
			w.st.addLog(fmt.Sprintf("HLS-проверка выполнена: плейлист и HEAD сегментов (%.1f с)", time.Since(started).Seconds()))
		}
		if waitForWork(nextHLS) {
			return nil
		}
	}
	return nil
}

// Count ready rewards, but announce only a transition from confirmed unfinished
// minutes. Already-ready rewards discovered at startup are silent baselines.
func (w *accountWorker) markReady(drop twitch.Drop) {
	w.st.mu.Lock()
	if w.st.readyDrops == nil {
		w.st.readyDrops = make(map[string]struct{})
	}
	if _, exists := w.st.readyDrops[drop.ID]; exists {
		w.st.mu.Unlock()
		return
	}
	w.st.readyDrops[drop.ID] = struct{}{}
	w.st.DropsReady++
	_, announce := w.st.pendingDrops[drop.ID]
	announce = announce && !drop.IsClaimed
	delete(w.st.pendingDrops, drop.ID)
	w.st.recordRewardProgressLocked(drop, announce)
	w.st.mu.Unlock()
	if announce {
		w.st.addLog(fmt.Sprintf("✅ ВРЕМЯ НАБРАНО: %s (%d/%d мин); получение вручную", drop.Name, drop.Current, drop.Required))
	}
	w.m.recordReady(w.acc, drop, announce)
}

func (w *accountWorker) findDrop(ctx context.Context, dropID string) (twitch.Drop, bool, error) {
	drops, err := w.gql.DropsDashboard(ctx)
	if err != nil {
		return twitch.Drop{}, false, err
	}
	for _, d := range drops {
		if d.ID == dropID && (w.m.cfg.CampaignID == "" || d.CampaignID == w.m.cfg.CampaignID) {
			return d, true, nil
		}
	}
	return twitch.Drop{}, false, nil
}

// --- helpers ---

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func sleepUntil(ctx context.Context, deadline time.Time) bool {
	return sleepCtx(ctx, time.Until(deadline))
}

// jittered возвращает base ± случайные [0..jitterSec] секунд.
func jittered(rnd *rand.Rand, base time.Duration, jitterSec int) time.Duration {
	if jitterSec <= 0 {
		return base
	}
	delta := time.Duration(rnd.Intn(2*jitterSec+1)-jitterSec) * time.Second
	d := base + delta
	if d < time.Second {
		d = time.Second
	}
	return d
}

// backoff — экспоненциальная пауза при повторных ошибках (30s..8m + разброс).
func backoff(errCount int, rnd *rand.Rand) time.Duration {
	shift := errCount
	if shift > 4 {
		shift = 4
	}
	d := time.Duration(30*(1<<shift)) * time.Second
	if d > 8*time.Minute {
		d = 8 * time.Minute
	}
	return d + time.Duration(rnd.Intn(30))*time.Second
}

func isAuthError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "авторизац")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
