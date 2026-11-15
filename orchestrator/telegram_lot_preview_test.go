package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLotPreviewKeepsSelectionsSeparate(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["rust"] = &gameState{Name: "Rust", Campaigns: map[string]*campaign{
		"one": {ID: "one", Name: "First", Rewards: []rewardRequirement{{ID: "a"}}},
		"two": {ID: "two", Name: "Second", Rewards: []rewardRequirement{{ID: "b"}}},
	}}
	ref := gameRef("rust")
	for i, id := range []string{"one", "two"} {
		lot := strings.Repeat([]string{"a", "b"}[i], 24)
		s.telegramMenu("lot-link\n" + lot)
		s.telegramMenu("lot-preview-game:" + ref)
		s.telegramMenu("lot-toggle:" + ref + ":0:" + gameRef(id))
		s.telegramMenu("lot-preview-save:" + ref + ":0")
	}
	token := strings.Repeat("x", 32)
	t.Setenv("ORCHESTRATOR_LOT_PREVIEW_TOKEN", token)
	for lot, id := range map[string]string{strings.Repeat("a", 24): "one", strings.Repeat("b", 24): "two"} {
		req := httptest.NewRequest(http.MethodGet, "/internal/lot-preview?lot="+lot, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		s.serveLotPreview(res, req)
		var got lotPreviewSelection
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil || len(got.Campaigns) != 1 || got.Campaigns[0] != id {
			t.Fatalf("unexpected selection for %s: %d %+v", lot, res.Code, got)
		}
	}
}
