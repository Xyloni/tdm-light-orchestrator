package worker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

type shutdownControl struct {
	message string
}

func (c *shutdownControl) Run(ctx context.Context) { <-ctx.Done() }
func (c *shutdownControl) Send(ctx context.Context, message string) error {
	c.message = message
	return nil
}

func TestIdleShutdownChecksEveryGroupAndNotifiesBeforeExit(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame, cfg.LogDir = "Game", ""
	cfg.StopWhenIdle = true
	groups := [][]*parser.Account{{{Login: "first"}}, {{Login: "second"}}}
	m := NewManager(cfg, groups, make(chan struct{}, 1))
	control := &shutdownControl{}
	m.bot = bot.NewService(control)
	counts := map[string]int{}
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		counts[r.Header.Get("X-Device-Id")]++
		return reviewReply(completedGroupInventory), nil
	})
	for i, group := range groups {
		group[0].DeviceID = string(rune('a' + i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if m.runCycles(ctx, time.Millisecond) || ctx.Err() != nil {
		t.Fatal("autostop did not finish normally")
	}
	if counts["a"] != 3 || counts["b"] != 3 {
		t.Fatal("not all groups checked three times", counts)
	}
	if control.message == "" {
		t.Fatal("shutdown notification not delivered before return")
	}
}

func TestIdleShutdownDisabledKeepsCheckingAndAllowsReload(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame, cfg.LogDir = "Game", ""
	m := NewManager(cfg, [][]*parser.Account{{{Login: "fixture"}}}, make(chan struct{}, 1))
	count := 0
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		count++
		if count == 4 {
			m.reloadCh <- struct{}{}
		}
		return reviewReply(completedGroupInventory), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !m.runCycles(ctx, time.Millisecond) || count != 4 {
		t.Fatal("disabled autostop or reload failed", count)
	}
}

func TestIdleShutdownDoesNotTreatNetworkErrorAsFinished(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame, cfg.LogDir = "Game", ""
	cfg.StopWhenIdle, cfg.IdleCheckRounds = true, 1
	m := NewManager(cfg, [][]*parser.Account{{{Login: "fixture"}}}, make(chan struct{}, 1))
	started := make(chan struct{})
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		close(started)
		return nil, context.DeadlineExceeded
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- m.runCycles(ctx, time.Millisecond) }()
	<-started
	select {
	case <-done:
		t.Fatal("network error caused autostop")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	<-done
	if !m.cycleErrors.Load() {
		t.Fatal("network error not tracked")
	}
}

func TestAvailableFarmingMarksCycleBusy(t *testing.T) {
	m, workers, probes := signalFixture(t, 1, "")
	m.cfg.StopWhenIdle, m.cfg.IdleCheckRounds = true, 2
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { workers[0].Run(ctx); close(done) }()
	deadline := time.Now().Add(time.Second)
	for probes[0].calls[0].Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !m.cycleWorked.Load() {
		t.Fatal("available farming was counted as an empty cycle")
	}
}
