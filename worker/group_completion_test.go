package worker

import (
	"context"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

const completedGroupInventory = `{"data":{"currentUser":{"inventory":{"dropCampaignsInProgress":[{"id":"campaign","status":"ACTIVE","game":{"name":"Game"},"timeBasedDrops":[{"id":"first","name":"First","requiredMinutesWatched":60,"self":{"currentMinutesWatched":240,"isClaimed":false}},{"id":"second","name":"Second","requiredMinutesWatched":120,"self":{"currentMinutesWatched":240,"isClaimed":false}},{"id":"third","name":"Third","requiredMinutesWatched":180,"self":{"currentMinutesWatched":240,"isClaimed":false}},{"id":"fourth","name":"Fourth","requiredMinutesWatched":240,"self":{"currentMinutesWatched":240,"isClaimed":false}}]}]}}}}`

func TestLegacyGroupTimerDoesNotShortenSession(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	cfg.GroupSessionMinutes = 1
	acc := &parser.Account{Login: "fixture", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	m.http.Timeout = 0 // Inspect the group context, without the HTTP client's deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	requests := 0
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		actual, ok := r.Context().Deadline()
		if !ok || !actual.Equal(deadline) {
			t.Errorf("legacy group timer imposed a shorter deadline: %s", actual)
		}
		return reviewReply(completedGroupInventory), nil
	})
	if m.runGroup(ctx, []*parser.Account{acc}) {
		t.Fatal("unexpected reload")
	}
	if requests != 1 || m.states[acc.Login].DropsReady != 4 {
		t.Fatal("not all unclaimed rewards were counted")
	}
}

func TestNextGroupWaitsForAllRewardsOfCurrentGroup(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	cfg.GroupSessionMinutes = 60
	first := &parser.Account{Login: "firstfixture", UserID: "1", DeviceID: "first"}
	second := &parser.Account{Login: "secondfixture", UserID: "2", DeviceID: "second"}
	m := NewManager(cfg, [][]*parser.Account{{first}, {second}}, make(chan struct{}, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	m.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("X-Device-Id") {
		case "first":
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		case "second":
			state := m.states[first.Login]
			state.mu.Lock()
			ready := state.DropsReady
			state.mu.Unlock()
			if ready != 4 {
				t.Error("next group started before all four rewards were complete")
			}
			close(secondStarted)
			cancel()
		default:
			t.Error("unexpected account")
		}
		return reviewReply(completedGroupInventory), nil
	})
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first group did not start")
	}
	select {
	case <-secondStarted:
		t.Fatal("next group started while previous group was unfinished")
	case <-time.After(30 * time.Millisecond):
	}
	if m.currentGroup.Load() != 1 {
		t.Fatal("group index advanced early")
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("next group did not start after completion")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestFourthRewardMustReachAllRequiredMinutes(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	acc := &parser.Account{Login: "fixture", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	body := strings.Replace(completedGroupInventory, `"requiredMinutesWatched":240,"self":{"currentMinutesWatched":240`, `"requiredMinutesWatched":240,"self":{"currentMinutesWatched":239`, 1)
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) { return reviewReply(body), nil })
	w := &accountWorker{m: m, acc: acc, st: m.states[acc.Login], gql: twitch.NewGQLClient(m.http, cfg, acc)}
	drop, ok, err := w.nextDrop(context.Background())
	if err != nil || !ok || drop.ID != "fourth" || drop.Current != 239 || w.st.DropsReady != 3 {
		t.Fatal("unfinished fourth reward was treated as complete", drop, ok, err)
	}
	body = completedGroupInventory
	_, ok, err = w.nextDrop(context.Background())
	if err != nil || ok || w.st.DropsReady != 4 {
		t.Fatal("four completed unclaimed rewards did not finish the account", ok, err)
	}
}

func TestAuthFailureDoesNotCountAsCompletedGroup(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	acc := &parser.Account{Login: "fixture", UserID: "1"}
	reload := make(chan struct{}, 1)
	m := NewManager(cfg, [][]*parser.Account{{acc}}, reload)
	m.states[acc.Login].authFailed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- m.runGroup(ctx, []*parser.Account{acc}) }()
	select {
	case <-done:
		t.Fatal("auth failure allowed unfinished group to advance")
	case <-time.After(20 * time.Millisecond):
	}
	reload <- struct{}{}
	select {
	case restarted := <-done:
		if !restarted {
			t.Fatal("reload did not unblock failed group")
		}
	case <-time.After(time.Second):
		t.Fatal("failed group cannot reload")
	}
}
