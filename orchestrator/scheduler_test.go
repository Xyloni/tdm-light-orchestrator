package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

func TestPlanGroupsEnlargesAsDeadlineApproaches(t *testing.T) {
	for _, tt := range []struct {
		left         time.Duration
		active, want int
	}{
		{24 * time.Hour, 0, 400},
		{24 * time.Hour, 300, 200},
		{6 * time.Hour, 0, 800},
	} {
		if got := planGroup(800, tt.active, 500, tt.left, 5*time.Hour, false); got != tt.want {
			t.Errorf("left %s, active %d: group = %d, want %d", tt.left, tt.active, got, tt.want)
		}
	}
}

func TestResourceModeUsesSmallestGroupThatFitsTheForecast(t *testing.T) {
	for _, tt := range []struct {
		left    time.Duration
		economy bool
		want    int
	}{
		{6 * time.Hour, false, 30},
		{6 * time.Hour, true, 10},
		{4 * time.Hour, true, 15},
		{2 * time.Hour, true, 30},
	} {
		if got := planGroup(30, 0, 1000, tt.left, 2*time.Hour, tt.economy); got != tt.want {
			t.Errorf("left %s, economy %t: got %d, want %d", tt.left, tt.economy, got, tt.want)
		}
	}
}

func testScheduler(t *testing.T, now time.Time) *scheduler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return &scheduler{state: state, path: path, miner: *config.Default(), active: make(map[string]*activeGame),
		lastDiscovery: now, notify: func(string) {}, retry: make(map[string]time.Time)}
}

func TestSeriesUsesSameProcessThenReleasesDuringPause(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	s.state.Settings.MaxGames = 1
	one := &campaign{ID: "first", Finished: "завершена", Ends: now.Add(time.Hour)}
	two := &campaign{ID: "second", Starts: now.Add(-time.Hour), Ends: now.Add(3 * time.Hour), Minutes: 30}
	three := &campaign{ID: "third", Starts: now.Add(4 * time.Hour), Ends: now.Add(8 * time.Hour), Minutes: 30}
	game := &gameState{Name: "Game", Target: 1, Current: one.ID, Campaigns: map[string]*campaign{one.ID: one, two.ID: two, three.ID: three}}
	s.state.Games["game"] = game
	s.state.Accounts = []account{{Credentials: parser.Account{Login: "player"}, Game: "game"}}
	s.airtimes = map[string]*campaignAirtime{"game": {campaignID: two.ID, at: now, next: now.Add(time.Minute), streams: []twitch.Stream{{Login: "eligible"}}}}
	stopped := false
	process := &minerProcess{commands: make(chan minerCommand, 4), stop: func() { stopped = true }}
	s.active["game"] = &activeGame{process: process}
	s.tick(context.Background(), now)
	if stopped || game.Current != two.ID || len(process.commands) != 1 {
		t.Fatal("active continuation should keep the miner")
	}
	group := <-process.commands
	if group.Config.CampaignID != two.ID || len(group.Streams) != 1 || group.Streams[0].Login != "eligible" {
		t.Fatalf("group targeted %s", group.Config.CampaignID)
	}
	s.update(processUpdate{game: "game", process: process, event: minerEvent{Done: true, Progress: []worker.ManagedProgress{{Login: "player", Status: "idle"}}}}, now)
	s.tick(context.Background(), now)
	s.tick(context.Background(), now)
	if !stopped || game.Current != "" || !game.SeriesPaused {
		t.Fatal("pause should release the slot and preserve continuation priority")
	}
}

func TestTrimPersistsMostDelayedAccountBeforeStoppingIt(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	s.state.Settings.MaxAccounts = 2
	c := &campaign{ID: "current", Minutes: 60, Group: []string{"fast", "slow", "middle"}}
	s.state.Games["game"] = &gameState{Current: c.ID, Campaigns: map[string]*campaign{c.ID: c}}
	process := &minerProcess{commands: make(chan minerCommand, 4)}
	s.active["game"] = &activeGame{process: process, groupStarted: now, progress: []worker.ManagedProgress{
		{Login: "fast", Required: 60, Current: 55, RateKnown: true, Rate: 0.9},
		{Login: "slow", Required: 60, Current: 50, RateKnown: true, Rate: 0.1},
		{Login: "middle", Required: 60, Current: 30, RateKnown: true, Rate: 0.8},
	}}
	s.trim(false)
	s.trim(true)
	select {
	case cmd := <-process.commands:
		if len(cmd.Remove) != 1 || cmd.Remove[0] != "slow" {
			t.Fatalf("wrong selection: %v", cmd.Remove)
		}
	default:
		t.Fatal("no removal sent")
	}
	reloaded, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Games["game"].Campaigns[c.ID].Results["slow"] != "trimmed" {
		t.Fatal("trim intent lost on restart")
	}
}

func TestPoolAssignmentDoesNotMoveAccountsFromAnotherGame(t *testing.T) {
	s := &state{Games: map[string]*gameState{"a": {Target: 3}}, Accounts: []account{
		{Credentials: parser.Account{Login: "mine"}, Game: "a"},
		{Credentials: parser.Account{Login: "theirs"}, Game: "b"},
		{Credentials: parser.Account{Login: "free"}},
	}}
	if !assignPool(s, "a") || s.Accounts[2].Game != "a" || s.Accounts[1].Game != "b" {
		t.Fatal("unexpected assignments")
	}
	if got := len(pending(s, "a", &campaign{})); got != 2 {
		t.Fatalf("launchable accounts = %d, want 2", got)
	}
}
