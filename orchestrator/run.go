package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"

	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/internal/dailylog"
)

var errRestartRequested = errors.New("orchestrator restart requested")

func Run(ctx context.Context, dir, address string) error {
	for {
		err := runOnce(ctx, dir, address)
		if !errors.Is(err, errRestartRequested) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func runOnce(parent context.Context, dir, address string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cfg, err := config.LoadSettings("config.json", "config.env")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "state.json")
	current, err := loadState(path)
	if err != nil {
		return err
	}
	logs := filepath.Join(dir, "logs")
	journal := &dailylog.Writer{Dir: logs, Name: "orchestrator"}
	defer journal.Close()
	s := &scheduler{
		journal: log.New(journal, "", log.LstdFlags),
		path:    path, state: current, miner: *cfg, active: make(map[string]*activeGame),
		updates: make(chan processUpdate, 128), catalogs: make(chan discoveryResult, 1),
		retry: make(map[string]time.Time), botChanges: make(chan struct{}, 1),
		restartRequests: make(chan struct{}, 1),
	}
	host, _, err := net.SplitHostPort(address)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("веб-панель требует loopback-адрес, например 127.0.0.1:8787")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	handler, err := webHandler(s)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second}
	defer server.Close()
	webDone := make(chan error, 1)
	botCtx, stopBots := context.WithCancel(ctx)
	botsDone := make(chan struct{})
	defer func() {
		stopBots()
		<-botsDone
	}()
	bots := &botRuntime{}
	s.notify = bots.notify
	s.initializeStats(time.Now())
	s.event("orchestrator_started")
	go func() {
		defer close(botsDone)
		s.runBots(botCtx, bots)
	}()
	go func() { webDone <- server.Serve(listener) }()
	prune := func() {
		if err := dailylog.Prune(logs, time.Now()); err != nil {
			log.Printf("log retention: %v", err)
		}
	}
	prune()
	s.mu.Lock()
	s.sampleResources(dir, time.Now())
	s.mu.Unlock()
	resources := time.NewTicker(resourceInterval)
	defer resources.Stop()
	digest := time.NewTicker(time.Minute)
	defer digest.Stop()
	retention := time.NewTicker(time.Hour)
	defer retention.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var stopReason error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.restartRequests:
			stopReason = errRestartRequested
			break loop
		case err := <-webDone:
			stopReason = err
			break loop
		case now := <-digest.C:
			s.mu.Lock()
			s.sendDailyDigest(now)
			s.sendDeadlineReports(now)
			s.mu.Unlock()
		case now := <-resources.C:
			s.mu.Lock()
			s.sampleResources(dir, now)
			s.mu.Unlock()
		case <-retention.C:
			prune()
		case t := <-ticker.C:
			s.mu.Lock()
			s.tick(ctx, t)
			s.mu.Unlock()
		case r := <-s.catalogs:
			s.mu.Lock()
			s.applyCatalog(r, time.Now())
			s.mu.Unlock()
		case u := <-s.updates:
			s.mu.Lock()
			s.update(u, time.Now())
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	for _, a := range s.active {
		a.stopping = true
		a.process.stop()
	}
	s.mu.Unlock()
	for len(s.active) > 0 {
		u := <-s.updates
		s.mu.Lock()
		s.update(u, time.Now())
		s.mu.Unlock()
	}
	cancel()
	if errors.Is(stopReason, errRestartRequested) {
		shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), 25*time.Second)
		defer stopShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("остановка веб-панели перед перезапуском: %w", err)
		}
	}
	return stopReason
}
