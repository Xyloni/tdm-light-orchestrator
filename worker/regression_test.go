package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reviewReply(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestReviewReadyDropLeftForManualClaim(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	acc := &parser.Account{Login: "test", UserID: "1"}
	h := &http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
		return reviewReply(`{"data":{"currentUser":{"inventory":{"dropCampaignsInProgress":[{"status":"ACTIVE","game":{"name":"Game"},"timeBasedDrops":[{"id":"drop","requiredMinutesWatched":60,"self":{"currentMinutesWatched":60,"isClaimed":false,"dropInstanceID":"ready"}}]}]}}}}`), nil
	})}
	m := &Manager{cfg: cfg}
	w := &accountWorker{m: m, acc: acc, st: &AccountState{Account: acc}, gql: twitch.NewGQLClient(h, cfg, acc)}
	_, ok, err := w.nextDrop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("completed drop selected for more farming")
	}
	if w.st.DropsReady != 1 || m.totalReady.Load() != 1 {
		t.Fatal("completed drop was not counted")
	}
	if _, _, err := w.nextDrop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.totalReady.Load() != 1 {
		t.Fatal("completed drop counted twice")
	}
}

func TestReviewAuthFailureDoesNotHotLoop(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	cfg.TargetGame = "Game"
	acc := &parser.Account{Login: "test", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	var calls atomic.Int64
	m.http = &http.Client{Transport: reviewTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reviewReply(`{"data":{"currentUser":null}}`), nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m.Run(ctx)
	if n := calls.Load(); n > 2 {
		t.Fatalf("stopped auth worker restarted %d times in 20ms", n)
	}
}

func TestReviewUploadValidationAndReplace(t *testing.T) {
	target := filepath.Join(t.TempDir(), "account.txt")
	original := []byte("e:original:p:token:1:device\n")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("bad"), []byte("# no accounts"), []byte(strings.Repeat("x", (2<<20)+1))} {
		if err := replaceAccountsFile(target, bad); err == nil {
			t.Fatal("invalid upload accepted")
		}
		got, _ := os.ReadFile(target)
		if string(got) != string(original) {
			t.Fatal("invalid upload overwrote original")
		}
	}
	if err := replaceAccountsFile(target, []byte("e:new:p:token:2:device\n")); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCancelLargeGroup(t *testing.T) {
	cfg := config.Default()
	cfg.LogDir = ""
	group := make([]*parser.Account, 10000)
	for i := range group {
		group[i] = &parser.Account{Login: fmt.Sprint(i)}
	}
	m := NewManager(cfg, [][]*parser.Account{group}, make(chan struct{}, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.runGroup(ctx, group) {
		t.Fatal("unexpected reload")
	}
}
