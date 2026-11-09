package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

type summaryControl struct{ messages chan string }

func (c *summaryControl) Run(ctx context.Context) { <-ctx.Done() }
func (c *summaryControl) Send(ctx context.Context, message string) error {
	select {
	case c.messages <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func summaryFixture(n int) (*Manager, []*accountWorker, *summaryControl) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	accounts := make([]*parser.Account, n)
	for i := range accounts {
		accounts[i] = &parser.Account{Login: fmt.Sprintf("summaryfixture%d", i)}
	}
	m := NewManager(cfg, [][]*parser.Account{accounts}, make(chan struct{}, 1))
	control := &summaryControl{messages: make(chan string, 8)}
	m.bot = bot.NewService(control)
	workers := make([]*accountWorker, n)
	for i, acc := range accounts {
		workers[i] = &accountWorker{m: m, acc: acc, st: m.states[acc.Login]}
	}
	return m, workers, control
}

func Test500ReadyAccountsProduceOneGroupSummary(t *testing.T) {
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)
	m, workers, _ := summaryFixture(500)
	drop := twitch.Drop{ID: "reward", CampaignID: "campaign", Name: "Reward", Required: 60, Current: 59, ProgressKnown: true}
	var wg sync.WaitGroup
	for _, w := range workers {
		w.st.setProgress(drop)
		wg.Add(1)
		go func(w *accountWorker) {
			defer wg.Done()
			ready := drop
			ready.Current = 60
			w.markReady(ready)
			w.markReady(ready)
		}(w)
	}
	wg.Wait()
	messages := m.readySummaries(true)
	if len(messages) != 1 || !strings.Contains(messages[0], "500/500") || !strings.Contains(messages[0], "новых: 500") {
		t.Fatalf("not grouped: %v", messages)
	}
	if m.totalReady.Load() != 500 || len(m.readySummaries(false)) != 0 {
		t.Fatal("duplicates counted or summary repeated")
	}
}

func TestHistoricalRewardsAreSilentAcrossRecreatedManagers(t *testing.T) {
	drop := twitch.Drop{ID: "reward", CampaignID: "campaign", Name: "Reward", Required: 60, Current: 60, ProgressKnown: true}
	for restart := 0; restart < 2; restart++ {
		m, workers, _ := summaryFixture(20)
		for _, w := range workers {
			w.markReady(drop)
			if strings.Contains(strings.Join(w.st.tailLogs(100), "\n"), "ВРЕМЯ НАБРАНО") {
				t.Fatal("historical reward generated a new completion log")
			}
		}
		if m.totalReady.Load() != 20 || len(m.readySummaries(false)) != 0 {
			t.Fatal("baseline generated notifications or disappeared from status")
		}
		select {
		case <-m.rewardWake:
			t.Fatal("baseline woke notification sender")
		default:
		}
	}
}

func TestPartialAndFinalSummariesOnlyContainNewChanges(t *testing.T) {
	m, workers, _ := summaryFixture(3)
	drop := twitch.Drop{ID: "reward", Name: "Reward", Required: 60, Current: 60, ProgressKnown: true}
	workers[0].markReady(drop) // Already ready before this run.
	pending := drop
	pending.Current = 59
	workers[1].st.setProgress(pending)
	workers[1].markReady(drop)
	if len(m.readySummaries(true)) != 0 {
		t.Fatal("partial progress sent as completed group")
	}
	partial := m.readySummaries(false)
	if len(partial) != 1 || !strings.Contains(partial[0], "2/3") || !strings.Contains(partial[0], "новых: 1") {
		t.Fatal("bad partial summary", partial)
	}
	if len(m.readySummaries(false)) != 0 {
		t.Fatal("unchanged partial summary repeated")
	}
	workers[2].st.setProgress(pending)
	workers[2].markReady(drop)
	final := m.readySummaries(true)
	if len(final) != 1 || !strings.Contains(final[0], "3/3") || !strings.Contains(final[0], "новых: 1") {
		t.Fatal("bad completion summary", final)
	}
}

func TestUnknownInitialProgressDoesNotAnnounceOldCompletion(t *testing.T) {
	m, workers, _ := summaryFixture(1)
	w := workers[0]
	drop := twitch.Drop{ID: "reward", Name: "Reward", Required: 60}
	w.st.setProgress(drop)
	drop.ProgressKnown = true
	drop.Current = 60
	w.markReady(drop)
	if len(m.readySummaries(false)) != 0 {
		t.Fatal("unknown baseline turned into false new completion")
	}
}

func TestCompleteRewardSummaryWakesSenderAndShutsDown(t *testing.T) {
	m, workers, control := summaryFixture(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	senderDone := make(chan struct{})
	summaryDone := make(chan struct{})
	go func() { m.bot.Run(ctx); close(senderDone) }()
	go func() { m.rewardSummaryLoop(ctx); close(summaryDone) }()
	drop := twitch.Drop{ID: "reward", Name: "Reward", Required: 60, Current: 59, ProgressKnown: true}
	workers[0].st.setProgress(drop)
	drop.Current = 60
	workers[0].markReady(drop)
	select {
	case message := <-control.messages:
		if !strings.Contains(message, "1/1") {
			t.Fatal("bad notification")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completed group waited for minute ticker")
	}
	cancel()
	for _, done := range []chan struct{}{senderDone, summaryDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("summary/sender loop leaked")
		}
	}
}

func TestSameRewardNamesAndDifferentGroupsRemainSeparate(t *testing.T) {
	m, workers, _ := summaryFixture(1)
	for i, required := range []int{60, 120, 180, 240} {
		d := twitch.Drop{ID: fmt.Sprint(i), CampaignID: "campaign", Name: "Supply Crate", Required: required, Current: required - 1, ProgressKnown: true}
		workers[0].st.setProgress(d)
		d.Current = required
		workers[0].markReady(d)
	}
	summaries := m.readySummaries(true)
	if len(summaries) != 4 {
		t.Fatal("different reward IDs were merged by name")
	}
	for i, required := range []int{60, 120, 180, 240} {
		if !strings.Contains(summaries[i], fmt.Sprintf("цель %d мин", required)) {
			t.Fatal("reward target missing", summaries[i])
		}
	}
	// The same drop in another group is a separate milestone.
	acc := &parser.Account{Login: "othergroupfixture"}
	m.groups = append(m.groups, []*parser.Account{acc})
	m.accountGroup[acc.Login] = 1
	m.recordReady(acc, twitch.Drop{ID: "0", CampaignID: "campaign", Name: "Supply Crate", Required: 60}, true)
	messages := m.readySummaries(true)
	if len(messages) != 1 || !strings.Contains(messages[0], "Группа 2") {
		t.Fatal("groups were merged", messages)
	}
}

func TestAlreadyClaimedRewardsContributeSilentlyToGroupCompletion(t *testing.T) {
	m, workers, _ := summaryFixture(2)
	drop := twitch.Drop{ID: "reward", Name: "Reward", Required: 60, Current: 60, ProgressKnown: true, IsClaimed: true}
	workers[0].markReady(drop)
	pending := drop
	pending.IsClaimed = false
	pending.Current = 59
	workers[1].st.setProgress(pending)
	drop.IsClaimed = false
	workers[1].markReady(drop)
	summaries := m.readySummaries(true)
	if len(summaries) != 1 || !strings.Contains(summaries[0], "2/2") || !strings.Contains(summaries[0], "новых: 1") {
		t.Fatal("claimed baseline prevented group summary", summaries)
	}
}
