package orchestrator

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

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
