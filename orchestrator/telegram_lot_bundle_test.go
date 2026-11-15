package orchestrator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func TestLotRegistrationKeepsSampleOutOfPersistentState(t *testing.T) {
	s := testScheduler(t, time.Now())
	ref := "0123456789abcdef01234567"
	s.state.LotCredentials = map[string]parser.Account{"short": {Login: "short", UserID: "21", OAuth: "existing-fixture"}}
	s.state.LotSold = map[string]bool{lotAccountReceipt("id", "51"): true}
	sample := []lotSampleAccount{{Login: "short"}, {Login: "full", UserID: "37", OAuth: "temporary-fixture", DeviceID: "device"}, {Login: "sold", UserID: "51", OAuth: "sold-fixture", DeviceID: "device"}}
	if !validLotSample(sample) {
		t.Fatal("valid sample rejected")
	}
	if err := s.registerLotTitle(ref, "test listing", sample); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	accounts := s.credentialsForLotSample(ref, s.lotSamples[ref].accounts)
	s.mu.Unlock()
	if len(accounts) != 2 || accounts[0].OAuth != "existing-fixture" || accounts[1].UserID != "37" {
		t.Fatal("incorrect stock account selection")
	}
	saved, err := os.ReadFile(s.path)
	if err != nil || strings.Contains(string(saved), "temporary-fixture") || strings.Contains(string(saved), "sold-fixture") {
		t.Fatal("sample leaked into persisted state")
	}
	if len(s.state.LotAccounts) > 0 || len(s.state.ManagedLots) > 0 {
		t.Fatal("registration moved an account or saved a bundle")
	}
}

func TestLotSearchFindsPastCampaignAndKeepsInventoryResultsDistinct(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["sea of thieves"] = &gameState{Name: "Sea of Thieves", Campaigns: map[string]*campaign{
		"past": {ID: "past", Name: "Season 21 Drops 3", Ends: time.Now().Add(-time.Hour), Rewards: []rewardRequirement{{ID: "old-reward", Name: "Reward"}}},
	}}
	count, err := s.discoverLotCampaigns("SEASON 21 DROPS 3", "")
	if err != nil || count != 1 || s.state.LotCatalog["past"].Rewards[0].ID != "old-reward" {
		t.Fatalf("historical match count=%d error=%v", count, err)
	}
	item := twitch.LotCampaign{ID: "inventory", Name: "Earlier campaign", TimeBasedDrops: []twitch.LotCampaignDrop{{ID: "first", Name: "First"}}}
	item.Game.Name = "Sea of Thieves"
	if count, err = s.importLotCampaigns("EARLIER", []twitch.LotCampaign{item}, true); err != nil || count != 1 {
		t.Fatalf("inventory import count=%d error=%v", count, err)
	}
	item.TimeBasedDrops = []twitch.LotCampaignDrop{{ID: "second", Name: "Second"}}
	if count, err = s.importLotCampaigns("Sea of Thieves", []twitch.LotCampaign{item}, true); err != nil || count != 1 {
		t.Fatalf("second inventory import count=%d error=%v", count, err)
	}
	if got := s.state.LotCatalog["inventory"]; !got.InventoryOnly || len(got.Rewards) != 2 {
		t.Fatalf("incorrect merged entry %+v", got)
	}
}

func TestTelegramLotBundleSelectsRewardsWithoutIDs(t *testing.T) {
	s := testScheduler(t, time.Now())
	for _, text := range []string{"Test Game\none | First\nalpha | Same Name\nbeta | Same Name", "Test Game\ntwo | Second\ngamma | Other"} {
		if err := s.saveManagedLotInput("lot-catalog", text); err != nil {
			t.Fatal(err)
		}
	}
	title := "Example listing"
	digest := sha256.Sum256([]byte(title))
	ref := fmt.Sprintf("%x", digest[:12])
	auth := strings.Repeat("x", 32)
	t.Setenv("ORCHESTRATOR_LOT_PREVIEW_TOKEN", auth)
	registration, err := json.Marshal(managedLotRequest{Operation: "register", Lot: ref, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/managed-lot", strings.NewReader(string(registration)))
	r.Header.Set("Authorization", "Bearer "+auth)
	w := httptest.NewRecorder()
	s.serveManagedLot(w, r)
	if w.Code != http.StatusOK || len(s.state.ManagedLots) != 0 {
		t.Fatalf("status=%d, active lots=%d", w.Code, len(s.state.ManagedLots))
	}

	press := func(panel bot.SettingsPanel, label string) bot.SettingsPanel {
		t.Helper()
		for _, row := range panel.Buttons {
			for _, button := range row {
				if len("control:"+button.Action) > 64 {
					t.Fatalf("long callback %q", button.Action)
				}
				if strings.Contains(button.Label, label) {
					return s.telegramMenu(button.Action)
				}
			}
		}
		t.Fatalf("button %q missing from %+v", label, panel)
		return bot.SettingsPanel{}
	}
	panel := press(s.telegramMenu("lb-list:0"), title)
	panel = press(panel, "Test Game")
	panel = press(panel, "First")
	panel = press(panel, "2. Same Name")
	panel = press(panel, "К кампаниям")
	panel = press(panel, "Second")
	panel = press(panel, "Выбрать все награды")
	panel = press(panel, "К кампаниям")
	panel = press(panel, "Проверить состав")
	if len(s.state.ManagedLots) != 0 || !strings.Contains(panel.Text, "2. Same Name") {
		t.Fatalf("incorrect preview: %s", panel.Text)
	}
	panel = press(panel, "Сохранить состав")
	if !strings.Contains(panel.Text, "Состав сохранён") {
		t.Fatalf("save failed: %s", panel.Text)
	}
	lot := s.state.ManagedLots[ref]
	if len(lot.Rewards) != 2 || lot.Rewards[0].ID != "beta" || lot.Rewards[1].ID != "gamma" {
		t.Fatalf("wrong rewards: %+v", lot.Rewards)
	}
	panel = press(s.telegramMenu("lb-list:0"), title)
	if !strings.Contains(press(panel, "First").Text, "1 из 2") {
		t.Fatal("saved partial selection not restored")
	}
}
