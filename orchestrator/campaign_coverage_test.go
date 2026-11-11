package orchestrator

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestCampaignCoverageAndConfirmedTelegramExport(t *testing.T) {
	s := testScheduler(t, time.Now())
	g := &gameState{Name: "Example", Campaigns: map[string]*campaign{
		"swiss": {ID: "swiss", Name: "Swiss", Rewards: []rewardRequirement{{ID: "reward", Minutes: 60}},
			Results: map[string]string{"legacy": "idle"}},
		"final": {ID: "final", Name: "Final", Results: map[string]string{"ready": "idle", "partial": "idle"}},
	}}
	s.state.Games["example"] = g
	for _, login := range []string{"ready", "claimed", "legacy", "partial"} {
		s.state.Accounts = append(s.state.Accounts, account{Game: "example", Credentials: parser.Account{Login: login, UserID: login, Password: "fixture-" + login}})
	}
	s.state.Stats.Progress = map[string]rewardWatermark{
		"example\x00swiss\x00ready\x00reward":   {Earned: true},
		"example\x00swiss\x00claimed\x00reward": {Earned: true, Claimed: true},
		"example\x00swiss\x00partial\x00reward": {Known: true, Minutes: 30},
	}
	c := s.coverage("example", g)
	if got := c.Campaigns[1]; got.Complete != 3 || got.Ready != 1 || got.Claimed != 1 || got.Unknown != 1 {
		t.Fatalf("unexpected coverage: %+v", got)
	}
	for mode, want := range map[string]int{"all": 1, "any": 4, "exact": 1} {
		items, err := c.selectAccounts([]string{"swiss", "final"}, mode)
		if err != nil || len(items) != want {
			t.Fatalf("%s: %d accounts, error %v, want %d", mode, len(items), err, want)
		}
	}
	ref := gameRef("example")
	s.telegramMenu("cov-export:" + ref)
	s.telegramMenu("cov-toggle:" + ref + ":" + gameRef("swiss"))
	panel := s.telegramMenu("cov-mode:" + ref + ":exact")
	if panel.Document != nil || !strings.Contains(panel.Text, "Подходят: 2 / 4") {
		t.Fatalf("unexpected preview: %q", panel.Text)
	}
	action := panel.Buttons[0][0].Action
	if !strings.HasPrefix(action, "cov-confirm:") || len("control:"+action) > 64 {
		t.Fatalf("unexpected confirm action %q", action)
	}
	if file := s.telegramMenu(action).Document; file == nil || !strings.Contains(file.Name, "Example") || strings.Contains(string(file.Data), ":ready:") ||
		!strings.Contains(string(file.Data), ":legacy:") || !strings.Contains(string(file.Data), ":claimed:") {
		t.Fatal("exact export included the wrong set")
	}
	pairs := panel.Buttons[1][0].Action
	if file := s.telegramMenu(pairs).Document; file == nil || file.Name != "Логины и пароли — Example — Swiss.txt" ||
		string(file.Data) != "claimed:fixture-claimed\nlegacy:fixture-legacy\n" {
		t.Fatal("short Telegram export did not contain exactly the selected login and password pairs")
	}
	s.state.Accounts[1].Game = ""
	if s.telegramMenu(action).Document != nil {
		t.Fatal("old confirmation exported a changed selection")
	}
}

func TestWebCampaignExportRequiresConfirmationAndKeepsCredentialsOutOfPreview(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["example"] = &gameState{Name: "Example", Campaigns: map[string]*campaign{
		"swiss": {ID: "swiss", Name: "Swiss", Results: map[string]string{"player": "idle"}},
	}}
	s.state.Accounts = []account{{Game: "example", Credentials: parser.Account{Email: "fixture@example.invalid", Login: "player", Password: "fixture-password", OAuth: "fixture-private-value", UserID: "17", DeviceID: "fixture-device"}}}
	handler, err := webHandler(s)
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))
	csrf := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(page.Body.String())
	if len(csrf) != 2 || !strings.Contains(page.Body.String(), "Завершили 1 из 1") {
		t.Fatal("campaign coverage or CSRF token is missing from the page")
	}
	form := url.Values{"csrf": {csrf[1]}, "game": {"example"}, "mode": {"all"}, "campaign": {"swiss"}, "action": {"campaign-export"}}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	preview := post()
	fingerprint := regexp.MustCompile(`name="fingerprint" value="([a-f0-9]+)"`).FindStringSubmatch(preview.Body.String())
	if len(fingerprint) != 2 || strings.Contains(preview.Body.String(), "fixture-private-value") {
		t.Fatal("preview is missing confirmation or exposes credentials")
	}
	form.Set("action", "campaign-export-confirm")
	form.Set("fingerprint", fingerprint[1])
	res := post()
	mediaType, params, err := mime.ParseMediaType(res.Header().Get("Content-Disposition"))
	if err != nil || mediaType != "attachment" || params["filename"] != "Аккаунты — Example — Swiss.txt" || !strings.Contains(res.Body.String(), "fixture-private-value") {
		t.Fatal("confirmed export did not return the account file")
	}
	form.Set("format", "login-password")
	if res := post(); res.Header().Get("Content-Disposition") != "" {
		t.Fatal("a confirmation for the full export also authorized a short export")
	}
	form.Set("action", "campaign-export")
	preview = post()
	fingerprint = regexp.MustCompile(`name="fingerprint" value="([a-f0-9]+)"`).FindStringSubmatch(preview.Body.String())
	if len(fingerprint) != 2 || strings.Contains(preview.Body.String(), "fixture-password") {
		t.Fatal("preview is missing confirmation or exposes the password")
	}
	form.Set("action", "campaign-export-confirm")
	form.Set("fingerprint", fingerprint[1])
	res = post()
	mediaType, params, err = mime.ParseMediaType(res.Header().Get("Content-Disposition"))
	if err != nil || mediaType != "attachment" || params["filename"] != "Логины и пароли — Example — Swiss.txt" ||
		res.Body.String() != "player:fixture-password\n" {
		t.Fatal("confirmed short web export did not return just login and password")
	}

}
