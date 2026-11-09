package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Control is an optional management transport. It never owns farming workers.
type Control interface {
	Run(context.Context)
	Send(context.Context, string) error
}

// Service bounds notification memory and keeps all network waits out of workers.
type Service struct {
	control  Control
	queue    chan string
	sendSlot chan struct{}
	overflow atomic.Uint64
}

func NewService(c Control) *Service {
	return &Service{control: c, queue: make(chan string, 128), sendSlot: make(chan struct{}, 1)}
}

// SendAndWait waits for delivery through the same serialized sender, bypassing
// the best-effort notification queue. The caller supplies a shutdown deadline.
func (s *Service) SendAndWait(ctx context.Context, message string) error {
	select {
	case s.sendSlot <- struct{}{}:
		defer func() { <-s.sendSlot }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.control.Send(ctx, truncate(message, 1900))
}

func (s *Service) Notify(message string) {
	message = truncate(message, 1000)
	select {
	case s.queue <- message:
	default:
		s.overflow.Add(1)
	}
}

func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.notifications(ctx) }()
	s.control.Run(ctx)
	// A stopped management connection must not stop farming or its sender.
	<-ctx.Done()
	wg.Wait()
}

func (s *Service) notifications(ctx context.Context) {
	pending := ""
	for ctx.Err() == nil {
		message := pending
		pending = ""
		if message == "" {
			select {
			case <-ctx.Done():
				return
			case message = <-s.queue:
			}
		}
		// Short batching window combines bursts of completed rewards.
		if !sleepCtx(ctx, time.Second) {
			return
		}
		for n := 0; n < 2; n++ {
			select {
			case next := <-s.queue:
				if textUnits(message)+textUnits(next) > 1750 {
					pending = next
				} else {
					message += "\n\n" + next
				}
			default:
			}
			if pending != "" {
				break
			}
		}
		if lost := s.overflow.Swap(0); lost > 0 {
			message += fmt.Sprintf("\n\nЕщё уведомлений: %d. Подробности: /status и /logs.", lost)
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.SendAndWait(attemptCtx, message)
		cancel()
		if err != nil && ctx.Err() == nil {
			// Details remain available in account logs/status even if delivery fails.
			log.Println("[bot] уведомление не доставлено; подробности доступны в /status и /logs")
		}
	}
}

func truncate(s string, limit int) string {
	r := []rune(strings.ToValidUTF8(s, "�"))
	units := 0
	for i, c := range r {
		units++
		if c > 0xFFFF {
			units++
		}
		if units > limit-1 && i < len(r)-1 {
			return string(r[:i]) + "…"
		}
		if units > limit {
			return string(r[:i]) + "…"
		}
	}
	return string(r)
}

func textUnits(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}
