package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLotRegistrationReturnsOptionalTelegramUsernameToCardinal(t *testing.T) {
	s := testScheduler(t, time.Now())
	const title = "Example listing"
	const auth = "fixture-token-with-32-characters"
	t.Setenv("ORCHESTRATOR_LOT_PREVIEW_TOKEN", auth)
	hash := sha256.Sum256([]byte(title))
	ref := fmt.Sprintf("%x", hash[:12])
	payload, err := json.Marshal(managedLotRequest{Operation: "register", Lot: ref, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	register := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/managed-lot", strings.NewReader(string(payload)))
		req.Header.Set("Authorization", "Bearer "+token)
		result := httptest.NewRecorder()
		s.serveManagedLot(result, req)
		return result
	}
	const username = "DropsManagerBot"
	s.telegramUsername = func(context.Context) string { return username }
	if denied := register("invalid"); denied.Code != http.StatusNotFound || strings.Contains(denied.Body.String(), username) {
		t.Fatalf("unauthorized registration returned code %d and body %q", denied.Code, denied.Body.String())
	}
	decode := func(result *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var body map[string]string
		if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || result.Code != http.StatusOK || body["state"] != "registered" {
			t.Fatalf("invalid registration response: code %d, body %q, error %v", result.Code, result.Body.String(), err)
		}
		return body
	}
	if got := decode(register(auth))["telegram_username"]; got != username {
		t.Fatalf("telegram_username=%q, want %q", got, username)
	}
	s.telegramUsername = func(context.Context) string { return "" }
	if _, included := decode(register(auth))["telegram_username"]; included {
		t.Fatal("missing Telegram username should be omitted")
	}
}
