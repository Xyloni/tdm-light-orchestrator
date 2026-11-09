package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendAndWaitRespectsDeadlineWhileSenderBusy(t *testing.T) {
	control := &stalledControl{started: make(chan struct{})}
	service := NewService(control)
	firstCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- service.SendAndWait(firstCtx, "first") }()
	<-control.started
	finalCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := service.SendAndWait(finalCtx, "shutdown"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("busy sender ignored final deadline", err)
	}
	cancel()
	<-firstDone
	if len(service.sendSlot) != 0 {
		t.Fatal("sender slot leaked")
	}
}

type stalledControl struct{ started chan struct{} }

func (b *stalledControl) Run(ctx context.Context) { <-ctx.Done() }
func (b *stalledControl) Send(ctx context.Context, _ string) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}

func Test500AccountsNeverWaitForStalledBot(t *testing.T) {
	c := &stalledControl{started: make(chan struct{})}
	s := NewService(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Notify("start")
	select {
	case <-c.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sender not running")
	}
	var wg sync.WaitGroup
	wg.Add(500)
	start := time.Now()
	for n := 0; n < 500; n++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				s.Notify(strings.Repeat("x", 250))
			}
		}()
	}
	wg.Wait()
	if time.Since(start) > 2*time.Second {
		t.Fatal("farming waited for bot network")
	}
	if len(s.queue) != 128 || s.overflow.Load() != 9872 {
		t.Fatalf("memory/count bound: queued=%d overflow=%d", len(s.queue), s.overflow.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked sender did not stop")
	}
}
