package orchestrator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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
	panel = press(panel, "Просмотреть и сохранить")
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

func TestTelegramLotSearchOpensRewardsFromRegisteredDeepLink(t *testing.T) {
	s := testScheduler(t, time.Now())
	const ref = "0123456789abcdef01234567"
	open := "lb-start:" + ref
	if panel := s.telegramMenu(open); s.lotBundleDraft != nil || !strings.Contains(panel.Text, "Лот ещё не добавлен") {
		t.Fatalf("unregistered link opened an editor: %+v", panel)
	}
	if err := s.registerLotTitle(ref, "Example listing", nil); err != nil {
		t.Fatal(err)
	}
	addCatalogFixture(t, s, "Example Game", "one", "Example Campaign")
	editor := s.telegramMenu(open)
	search := s.telegramMenu(catalogAction(t, editor, "Найти кампанию"))
	matches := s.telegramMenu(search.Input + "\nexample campaign")
	rewards := s.telegramMenu(catalogAction(t, matches, "Example Campaign"))
	if !strings.Contains(rewards.Text, "Отмечено наград: 0 из 1") || s.lotBundleDraft.Game != "example game" {
		t.Fatalf("search result did not open rewards: %+v", rewards)
	}
	rewards = s.telegramMenu(catalogAction(t, rewards, "Выбрать все награды"))
	if !strings.Contains(rewards.Text, "Черновик · выбрано 1") || len(s.state.ManagedLots) != 0 {
		t.Fatalf("selection should update only the draft: %+v", rewards)
	}
}

func TestTelegramLotSearchFindsExactTwitchCampaignLink(t *testing.T) {
	now := time.Now()
	s := testScheduler(t, now)
	const (
		ref    = "0123456789abcdef01234567"
		target = "df576175-0f22-467e-8ba7-955603a16567"
		other  = "5ce10491-2607-4cfe-a3cd-30b36e8a76ac"
		game   = "Borderlands 4"
		name   = "Borderlands 4 x The Hunt"
		link   = "https://www.twitch.tv/drops/campaigns?dropID=" + target
	)
	if err := s.registerLotTitle(ref, "Borderlands 4 lot", []lotSampleAccount{{Login: "login_only"}}); err != nil {
		t.Fatal(err)
	}
	s.state.Games["borderlands 4"] = &gameState{Name: game, Campaigns: map[string]*campaign{
		target: {ID: target, Name: name, Ends: now.Add(-time.Hour), Rewards: []rewardRequirement{{ID: "target-drop", Name: "The Horns"}}},
		other:  {ID: other, Name: name + " Encore", Rewards: []rewardRequirement{{ID: "other-drop", Name: "Other reward"}}},
	}}
	editor := s.telegramMenu("lb-start:" + ref)
	search := s.telegramMenu(catalogAction(t, editor, "Найти кампанию"))
	s.telegramMenu(search.Input + "\nhttps://example.invalid/drops/campaigns?dropID=" + target)
	if len(s.state.LotCatalog) != 0 {
		t.Fatal("campaign was imported from an unrelated URL")
	}
	matches := s.telegramMenu(search.Input + "\n" + link)
	if len(s.state.LotCatalog) != 1 || s.state.LotCatalog[target].Name != name {
		t.Fatalf("Twitch link didn't import exactly the requested campaign: %+v", s.state.LotCatalog)
	}
	rewards := s.telegramMenu(catalogAction(t, matches, "The Hunt"))
	if !strings.Contains(rewards.Text, "Отмечено наград: 0 из 1") {
		t.Fatalf("could not open the reward for the Twitch link: %+v", rewards)
	}
	delete(s.state.Games, "borderlands 4")
	matches = s.telegramMenu(search.Input + "\n" + target)
	catalogAction(t, matches, "The Hunt")
}

func TestTelegramLotSearchLooksUpTwitchLinkBeforeLotInventory(t *testing.T) {
	s := testScheduler(t, time.Now())
	const ref = "0123456789abcdef01234567"
	const id = "df576175-0f22-467e-8ba7-955603a16567"
	if err := s.registerLotTitle(ref, "Borderlands 4 lot", []lotSampleAccount{{Login: "login_only"}}); err != nil {
		t.Fatal(err)
	}
	s.state.LotCredentials = map[string]parser.Account{"catalog": {Login: "catalog", OAuth: "oauth-fixture", UserID: "21"}}
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var queries int
	http.DefaultTransport = managedLotDiagnosticTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			OperationName string
			Variables     map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		queries++
		if request.OperationName != "TDMManagedLotCampaign" || request.Variables["id"] != id {
			t.Fatalf("did not look up the campaign directly: %+v", request)
		}
		body := `{"data":{"currentUser":{"dropCampaign":{"id":"` + id + `","name":"Borderlands 4 x The Hunt","game":{"name":"Borderlands 4"},"timeBasedDrops":[{"id":"drop","name":"The Horns"}]}}}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	editor := s.telegramMenu("lb-start:" + ref)
	search := s.telegramMenu(catalogAction(t, editor, "Найти кампанию"))
	matches := s.telegramMenu(search.Input + "\nhttps://www.twitch.tv/drops/campaigns?dropID=" + id)
	catalogAction(t, matches, "The Hunt")
	if queries != 1 || s.state.LotCatalog[id].InventoryOnly {
		t.Fatalf("expected one direct catalog query, observed %d", queries)
	}
}
