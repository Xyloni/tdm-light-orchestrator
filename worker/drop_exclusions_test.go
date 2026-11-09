package worker

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func inventoryWithUnknownDrop(name string) string {
	return strings.Replace(completedGroupInventory, `"currentUser":{`, `"currentUser":{"dropCampaigns":[{"id":"other","name":"Other campaign","status":"ACTIVE","game":{"name":"Game"},"timeBasedDrops":[{"id":"white","name":"`+name+`","requiredMinutesWatched":60,"self":{"isClaimed":false}}]}],`, 1)
}

func TestExcludedHuntWhiteDoesNotBlockFourReadyRewards(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	acc := &parser.Account{Login: "fixture", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	body := inventoryWithUnknownDrop("Hunt White")
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) { return reviewReply(body), nil })
	w := &accountWorker{m: m, acc: acc, st: m.states[acc.Login], gql: twitch.NewGQLClient(m.http, cfg, acc)}
	_, ok, err := w.nextDrop(context.Background())
	if err != nil || ok || w.st.DropsReady != 4 {
		t.Fatal("unneeded catalog reward blocked completed group", ok, err)
	}
	cfg.IgnoredDropNames = nil
	drop, ok, err := w.nextDrop(context.Background())
	if err != nil || !ok || drop.Name != "Hunt White" || drop.ProgressKnown {
		t.Fatal("explicitly included reward was silently skipped", drop, ok, err)
	}
	cfg.IgnoredDropNames = []string{"Hunt White"}
	body = inventoryWithUnknownDrop("Other needed reward")
	drop, ok, err = w.nextDrop(context.Background())
	if err != nil || !ok || drop.Name != "Other needed reward" {
		t.Fatal("unknown progress was incorrectly treated as completed", drop, ok, err)
	}
}

func TestGroupReturnsAfterFourRewardsDespiteExcludedUnknownCatalogDrop(t *testing.T) {
	cfg := config.Default()
	cfg.TargetGame = "Game"
	cfg.LogDir = ""
	acc := &parser.Account{Login: "fixture", UserID: "1"}
	m := NewManager(cfg, [][]*parser.Account{{acc}}, make(chan struct{}, 1))
	m.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
		return reviewReply(inventoryWithUnknownDrop("Hunt White")), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if m.runGroup(ctx, []*parser.Account{acc}) || ctx.Err() != nil || m.states[acc.Login].DropsReady != 4 {
		t.Fatal("excluded catalog drop still holds the group")
	}
}

func TestExclusionsSaveWithoutLosingPreviousCommands(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")
	if err := os.WriteFile(file, []byte("TARGET_GAME=Game\nUNRELATED=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Other reward", "Name, with comma"} {
		if err := saveDropExclusion(file, name, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveDropExclusion(file, "other REWARD", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.json"), file)
	if err != nil || len(cfg.IgnoredDropNames) != 3 || !cfg.DropIgnored("Name, with comma") {
		t.Fatal("saved exclusions lost data", err)
	}
	for _, name := range []string{"Hunt White", "Other reward", "Name, with comma"} {
		if err := saveDropExclusion(file, name, false); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = config.Load(filepath.Join(dir, "config.json"), file)
	if err != nil || len(cfg.IgnoredDropNames) != 0 {
		t.Fatal("cannot remove all exclusions", err)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "UNRELATED=keep") {
		t.Fatal("unrelated config changed")
	}
	before := string(data)
	if err := saveDropExclusion(file, "bad\nIGNORED_DROP_NAMES=[]", true); err == nil {
		t.Fatal("config injection accepted")
	}
	after, _ := os.ReadFile(file)
	if string(after) != before {
		t.Fatal("invalid command modified config")
	}
	t.Setenv("IGNORED_DROP_NAMES", "Hunt White")
	if err := saveDropExclusion(file, "Other reward", true); err == nil {
		t.Fatal("environment override was silently ignored")
	}
}
