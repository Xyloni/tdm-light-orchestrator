package worker

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestTransientFailuresDoNotExcludeChannelsForFiveMinutes(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, errors.New("HTTP 503"), errors.New("HTTP 429"), errors.New("connection reset by peer")} {
		w := &accountWorker{}
		before := time.Now()
		duration := w.excludeFailedChannel("live", err, rand.New(rand.NewSource(1)))
		if duration < 20*time.Second || duration > 30*time.Second || w.unavailable["live"].After(before.Add(31*time.Second)) {
			t.Fatal("transient error received a hard cooldown", err, duration)
		}
	}
	for _, err := range []error{errors.New("HLS: HTTP 404"), errors.New("HLS: HTTP 403"), errors.New("playlist has no usable segment")} {
		w := &accountWorker{}
		if duration := w.excludeFailedChannel("live", err, rand.New(rand.NewSource(1))); duration != channelCooldown {
			t.Fatal("hard failure lost its cooldown", err, duration)
		}
	}
}

func Test500TransientFailuresHaveShortStaggeredRetries(t *testing.T) {
	delays := map[time.Duration]struct{}{}
	for n := 0; n < 500; n++ {
		w := &accountWorker{}
		rnd := rand.New(rand.NewSource(int64(n)))
		w.excludeFailedChannel("live", context.DeadlineExceeded, rnd)
		delay := channelRetryDelay(&channelRetryError{retryAt: w.unavailable["live"]}, 1, rnd)
		if delay < 19*time.Second || delay > 34*time.Second {
			t.Fatal("all-channel transient failure causes long wait", delay)
		}
		delays[delay.Round(time.Second)] = struct{}{}
	}
	if len(delays) < 5 {
		t.Fatal("retries are synchronized")
	}
}

func TestAllTemporarilyExcludedChannelsRecoverWhenCooldownEnds(t *testing.T) {
	_, workers, _ := signalFixture(t, 1, "healthy")
	w := workers[0]
	w.unavailable = map[string]time.Time{"live-0": time.Now().Add(25 * time.Second)}
	_, _, err := w.openStream(context.Background(), rand.New(rand.NewSource(1)))
	var retry *channelRetryError
	if !errors.As(err, &retry) || retry.attempts != 0 {
		t.Fatal("expected explicit cooldown retry", err)
	}
	delay := channelRetryDelay(err, 1, rand.New(rand.NewSource(1)))
	if delay > 29*time.Second {
		t.Fatal("all-channel cooldown became a five-minute stall", delay)
	}
	w.unavailable["live-0"] = time.Now().Add(-time.Second)
	stream, variant, err := w.openStream(context.Background(), rand.New(rand.NewSource(1)))
	if err != nil || stream.Login != "live-0" || !strings.HasPrefix(variant, "https://") {
		t.Fatal("recovered channel cannot be opened", err)
	}
	if _, exists := w.unavailable["live-0"]; exists {
		t.Fatal("successful channel retained expired cooldown")
	}
}

func TestRetryBackoffIsBoundedAndDirectoryStillRefreshes(t *testing.T) {
	for _, failures := range []int{0, 1, 2, 3, 100} {
		delay := channelRetryDelay(context.DeadlineExceeded, failures, rand.New(rand.NewSource(1)))
		if delay < 15*time.Second || delay > 70*time.Second {
			t.Fatal("unbounded retry backoff", delay)
		}
	}
	delay := channelRetryDelay(&channelRetryError{retryAt: time.Now().Add(5 * time.Minute)}, 100, rand.New(rand.NewSource(1)))
	if delay > 63*time.Second {
		t.Fatal("new streams not discovered while old streams are excluded", delay)
	}
}

func TestPlaylistRetryPreservesTransientCauseWithoutSignedURL(t *testing.T) {
	m, workers, _ := signalFixture(t, 1, "healthy")
	w := workers[0]
	base := m.http.Transport
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "test.ttvnw.net" {
			return nil, io.EOF
		}
		return base.RoundTrip(r)
	})
	_, err := w.gql.WatchHLS(context.Background(), "https://test.ttvnw.net/live-0.m3u8?token=fixture-secret", twitch.Stream{Login: "live-0"})
	if err == nil || !errors.Is(err, io.EOF) || !temporaryPlaybackError(err) {
		t.Fatal("playlist retry lost transient cause", err)
	}
	if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("signed URL leaked")
	}
}

func TestExpiredPlaylistWithTransientRefreshDoesNotGetHardBan(t *testing.T) {
	m, workers, _ := signalFixture(t, 1, "healthy")
	w := workers[0]
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		reply := reviewReply(`{}`)
		if r.URL.Host == "test.ttvnw.net" {
			reply.StatusCode = 404
		} else {
			reply.StatusCode = 503
		}
		return reply, nil
	})
	_, err := w.gql.WatchHLS(context.Background(), "https://test.ttvnw.net/live-0.m3u8", twitch.Stream{Login: "live-0"})
	if err == nil || !temporaryPlaybackError(err) {
		t.Fatal("refresh outage classified as permanent playlist failure", err)
	}
	if duration := w.excludeFailedChannel("live-0", err, rand.New(rand.NewSource(1))); duration > 30*time.Second {
		t.Fatal("temporary refresh got five-minute ban")
	}
}
