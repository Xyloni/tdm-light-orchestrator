package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

const channelCooldown = 5 * time.Minute

func (w *accountWorker) excludeChannel(login string) {
	w.cooldownChannel(login, channelCooldown)
}

func (w *accountWorker) cooldownChannel(login string, duration time.Duration) {
	if w.unavailable == nil {
		w.unavailable = make(map[string]time.Time)
	}
	// Remove expired entries, keeping per-account state bounded over long runs.
	now := time.Now()
	for channel, until := range w.unavailable {
		if !now.Before(until) {
			delete(w.unavailable, channel)
		}
	}
	until := now.Add(duration)
	if until.After(w.unavailable[login]) {
		w.unavailable[login] = until
	}
}

var playbackStatusRE = regexp.MustCompile(`HTTP (\d{3})`)

func temporaryPlaybackError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var network net.Error
	if errors.As(err, &network) {
		return true
	}
	if status := playbackStatusRE.FindStringSubmatch(err.Error()); status != nil {
		code, _ := strconv.Atoi(status[1])
		if code == 408 || code == 429 || (code >= 500 && code <= 599) {
			return true
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if temporaryPlaybackError(cause) {
				return true
			}
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if temporaryPlaybackError(wrapped.Unwrap()) {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unexpected eof") || strings.Contains(message, "connection reset") || strings.Contains(message, "connection force closed")
}

func (w *accountWorker) excludeFailedChannel(login string, err error, rnd *rand.Rand) time.Duration {
	duration := channelCooldown
	if temporaryPlaybackError(err) {
		duration = time.Duration(20+rnd.Intn(11)) * time.Second
	}
	w.cooldownChannel(login, duration)
	return duration
}

type channelRetryError struct {
	attempts int
	retryAt  time.Time
}

func (e *channelRetryError) Error() string {
	return fmt.Sprintf("каналы временно недоступны (проверено %d); повтор с учётом окончания исключения", e.attempts)
}

func channelRetryDelay(err error, failures int, rnd *rand.Rand) time.Duration {
	var retry *channelRetryError
	if errors.As(err, &retry) && !retry.retryAt.IsZero() {
		delay := maxDuration(time.Until(retry.retryAt), time.Second)
		if delay > time.Minute {
			delay = time.Minute
		} // Check the directory for new streams without probing excluded ones.
		return delay + time.Duration(rnd.Intn(4))*time.Second
	}
	if failures < 1 {
		failures = 1
	}
	if failures > 3 {
		failures = 3
	}
	base := time.Duration(15*(1<<(failures-1))) * time.Second
	return base + time.Duration(rnd.Intn(11))*time.Second
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (w *accountWorker) openStream(ctx context.Context, rnd *rand.Rand) (twitch.Stream, string, error) {
	return w.openStreamExcept(ctx, rnd, "")
}

func (w *accountWorker) openChannel(ctx context.Context, s twitch.Stream) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, channelOpenBudget)
	defer cancel()
	token, sig, err := w.gql.PlaybackAccessToken(ctx, s.Login)
	if err != nil {
		return "", err
	}
	master, err := w.gql.GetMasterPlaylist(ctx, twitch.MasterPlaylistURL(s.Login, token, sig))
	if err != nil {
		return "", err
	}
	return twitch.PickLowestVariant(master)
}

func (w *accountWorker) recoverSlowStream(ctx context.Context, rnd *rand.Rand, current twitch.Stream) (twitch.Stream, string, error) {
	ctx, cancel := context.WithTimeout(ctx, streamRecoveryBudget)
	defer cancel()
	w.excludeChannel(current.Login)
	selected, playlist, err := w.openStreamExcept(ctx, rnd, current.Login)
	if err == nil {
		w.st.addLog("смена канала из-за медленного начисления: " + current.Login + " → " + selected.Login)
		return selected, playlist, nil
	}
	if ctx.Err() != nil || isAuthError(err) {
		return twitch.Stream{}, "", err
	}
	playlist, err = w.openChannel(ctx, current)
	if err != nil {
		return twitch.Stream{}, "", err
	}
	delete(w.unavailable, current.Login)
	w.st.addLog("замены каналу " + current.Login + " нет; текущий просмотр переподключён")
	return current, playlist, nil
}

func (w *accountWorker) openStreamExcept(ctx context.Context, rnd *rand.Rand, excluded string) (twitch.Stream, string, error) {
	ctx, cancel := context.WithTimeout(ctx, streamRecoveryBudget)
	defer cancel()
	// Limit only expensive recovery, never normal HLS/beacon/Inventory signals.
	if w.m.recoverySlots != nil {
		queued := time.Now()
		select {
		case w.m.recoverySlots <- struct{}{}:
			if w.m.cfg.Diagnostics {
				w.st.addLog(fmt.Sprintf("diagnostic recovery queue_ms=%d active=%d", time.Since(queued).Milliseconds(), len(w.m.recoverySlots)))
			}
			defer func() { <-w.m.recoverySlots }()
		case <-ctx.Done():
			return twitch.Stream{}, "", ctx.Err()
		}
	}
	streams := w.m.campaignStreams
	if streams == nil {
		var err error
		streams, err = w.gql.TopStreams(ctx, w.m.cfg.TargetGame, 15)
		if err != nil {
			return twitch.Stream{}, "", err
		}
	}
	attempts := 0
	var earliest time.Time
	noteRetry := func(until time.Time) {
		if !until.IsZero() && (earliest.IsZero() || until.Before(earliest)) {
			earliest = until
		}
	}
	for _, i := range rnd.Perm(len(streams)) {
		s := streams[i]
		if s.Login == excluded {
			continue
		}
		if time.Now().Before(w.unavailable[s.Login]) {
			noteRetry(w.unavailable[s.Login])
			continue
		}
		if err := ctx.Err(); err != nil {
			return twitch.Stream{}, "", err
		}
		attempts++
		variant, err := w.openChannel(ctx, s)
		if err == nil {
			delete(w.unavailable, s.Login)
			return s, variant, nil
		}
		if ctx.Err() != nil {
			return twitch.Stream{}, "", ctx.Err()
		}
		if isAuthError(err) {
			return twitch.Stream{}, "", err
		}
		duration := w.excludeFailedChannel(s.Login, err, rnd)
		noteRetry(w.unavailable[s.Login])
		// Do not print signed playlist URLs from transport errors.
		w.st.addLog(fmt.Sprintf("не удалось открыть канал %s; пауза %s", s.Login, duration))
		if attempts >= 3 {
			break // Bound recovery work before the worker's normal retry pause.
		}
	}
	// The three-attempt cap may leave other candidates untried. Do not wait
	// five minutes just because the attempted candidates returned hard failures.
	if attempts > 0 {
		soon := time.Now().Add(15 * time.Second)
		if earliest.IsZero() || earliest.After(soon) {
			earliest = soon
		}
	}
	return twitch.Stream{}, "", &channelRetryError{attempts: attempts, retryAt: earliest}
}

// Stagger retries on the current stream before triggering expensive recovery.
func retryHLSDuration(interval time.Duration, failures int, rnd *rand.Rand) time.Duration {
	base := interval * time.Duration(failures)
	if base > time.Minute {
		base = time.Minute
	}
	return base + time.Duration(rnd.Int63n(maxInt64(1, int64(base/4))))
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
