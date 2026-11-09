package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPanelPersistsBotConfigurationWithoutRenderingToken(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.botChanges = make(chan struct{}, 1)
	handler, err := webHandler(s)
	if err != nil {
		t.Fatal(err)
	}
	const address = "http://localhost/"
	const secret = "fake-test-secret"
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, address, nil))
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(initial.Body.String())
	if len(match) != 2 {
		t.Fatal("missing CSRF field")
	}
	submit := func(platform, id, token string) string {
		t.Helper()
		values := url.Values{"csrf": {match[1]}, "action": {"bot"}, "platform": {platform}, "owner_id": {id}, "bot_token": {token}}
		request := httptest.NewRequest(http.MethodPost, address, strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK || strings.Contains(body, secret) {
			t.Fatalf("bad response status or token rendered: %d", recorder.Code)
		}
		return body
	}
	submit("telegram", "123", secret)
	select {
	case <-s.botChanges:
	default:
		t.Fatal("save did not signal supervisor")
	}
	submit("telegram", "456", "")
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Bot == nil || saved.Bot.OwnerID != "456" || saved.Bot.Token != secret {
		t.Fatal("owner change should preserve token")
	}
	submit("discord", "invalid", secret)
	if s.state.Bot.Platform != "telegram" {
		t.Fatal("invalid Discord ID changed active platform")
	}
	submit("discord", "123456789012345678", "another-secret")
	if s.state.Bot.Platform != "discord" {
		t.Fatal("valid Discord settings did not replace Telegram")
	}
	submit("none", "", "")
	saved, err = loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Bot == nil || saved.Bot.Platform != "none" || saved.Bot.Token != "" {
		t.Fatal("disable did not remove stored token")
	}
}
