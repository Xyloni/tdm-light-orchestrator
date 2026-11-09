package worker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestReviewNoDropsDoesNotHotLoop(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	m := NewManager(cfg, [][]*parser.Account{{{Login: "test"}}}, make(chan struct{}, 1))
	var calls atomic.Int64
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reviewReply(`{"data":{"currentUser":{"dropCampaigns":[],"inventory":{"dropCampaignsInProgress":[]}}}}`), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	m.Run(ctx)
	if calls.Load() != 1 {
		t.Fatalf("dashboard calls=%d, want 1", calls.Load())
	}
}

func TestReviewAuthFailureStaysDisabledBetweenSessions(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	acc := &parser.Account{Login: "test"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	var calls atomic.Int64
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reviewReply(`{"data":{"currentUser":null}}`), nil
	})
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		m.runGroup(ctx, []*parser.Account{acc})
		cancel()
	}
	if calls.Load() != 1 {
		t.Fatalf("authorization retried %d times", calls.Load())
	}
}

func TestReviewReloadInterruptsIdlePause(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	reload := make(chan struct{}, 1)
	m := NewManager(cfg, [][]*parser.Account{{{Login: "test"}}}, reload)
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		reload <- struct{}{}
		return reviewReply(`{"data":{"currentUser":{"dropCampaigns":[],"inventory":{"dropCampaignsInProgress":[]}}}}`), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !m.Run(ctx) {
		t.Fatal("reload blocked by idle pause")
	}
}

func TestReviewLogRingAndRotation(t *testing.T) {
	s := &AccountState{Account: &parser.Account{Login: "test"}}
	for i := 0; i < 250; i++ {
		s.addLog(fmt.Sprintf("line %d", i))
	}
	lines := s.tailLogs(100)
	if len(lines) != 100 || !strings.HasSuffix(lines[0], "line 150") || !strings.HasSuffix(lines[99], "line 249") {
		t.Fatalf("incorrect ring order: %v", lines)
	}
	if got := s.tailLogs(-1); len(got) != 0 {
		t.Fatal("negative tail length")
	}
	s.LogFile = filepath.Join(t.TempDir(), "test.log")
	for i := 0; i < 5; i++ {
		if err := s.appendFileLog(strings.Repeat("x", maxLogFileBytes/2)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{s.LogFile, s.LogFile + ".1"} {
		info, err := os.Stat(name)
		if err != nil || info.Size() > maxLogFileBytes {
			t.Fatalf("unbounded log %s: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(s.LogFile))
	if len(entries) != 2 {
		t.Fatalf("unexpected log generations: %d", len(entries))
	}
}

func TestReviewConcurrentLogsAndStatus(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	m := NewManager(cfg, [][]*parser.Account{{{Login: "test"}}}, make(chan struct{}, 1))
	s := m.states["test"]
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.addLog("line")
				s.setStatus("idle")
				_ = m.statusText()
				_ = m.logsText("TEST")
			}
		}()
	}
	wg.Wait()
	if len(s.tailLogs(1000)) != maxLogLines {
		t.Fatal("in-memory logs exceeded bound")
	}
}

func TestReviewGameEnvOverrideIsExplicit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.env")
	t.Setenv("TARGET_GAME", "Old")
	if err := saveTargetGame(p, "New"); err == nil {
		t.Fatal("environment override silently ignored")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("rejected change wrote config")
	}
	if err := saveTargetGame(p, "Old"); err != nil {
		t.Fatal(err)
	}
}
