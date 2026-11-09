package orchestrator

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPanelFormsAndGamePoolUpload(t *testing.T) {
	s := testScheduler(t, time.Now())
	handler, err := webHandler(s)
	if err != nil {
		t.Fatal(err)
	}
	const address = "http://localhost/"
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, address, nil))
	match := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(index.Body.String())
	if index.Code != http.StatusOK || len(match) != 2 || strings.Contains(index.Body.String(), `name="command"`) {
		t.Fatalf("expected dedicated panel forms, status %d", index.Code)
	}
	post := func(form url.Values) string {
		t.Helper()
		form.Set("csrf", match[1])
		request := httptest.NewRequest(http.MethodPost, address, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("form %s: %d", form.Get("action"), response.Code)
		}
		return response.Body.String()
	}
	body := post(url.Values{"action": {"add"}, "target": {"1000"}, "game": {"Example Game"}})
	if !strings.Contains(body, `<option value="example game">Example Game</option>`) {
		t.Fatal("new game missing from pool selector")
	}
	post(url.Values{"action": {"add"}, "target": {"100"}, "game": {"Other Game"}})
	post(url.Values{"action": {"limits"}, "max_games": {"3"}, "max_accounts": {"1200"}})
	body = post(url.Values{"action": {"mode"}, "save_resources": {"on"}})
	if !strings.Contains(body, "Текущий режим: Экономия ресурсов") {
		t.Fatal("mode missing from panel")
	}
	post(url.Values{"action": {"miner"}, "diagnostics": {"on"}, "single_log": {"on"}, "pubsub": {"on"}})
	post(url.Values{"action": {"miner"}, "single_log": {"on"}})
	upload := func(game, lines string) string {
		t.Helper()
		var body bytes.Buffer
		multi := multipart.NewWriter(&body)
		for field, value := range map[string]string{"csrf": match[1], "action": "upload", "game": game} {
			if err := multi.WriteField(field, value); err != nil {
				t.Fatal(err)
			}
		}
		file, err := multi.CreateFormFile("accounts", "accounts.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(lines)); err != nil {
			t.Fatal(err)
		}
		if err = multi.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, address, &body)
		request.Header.Set("Content-Type", multi.FormDataContentType())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("upload: %d", response.Code)
		}
		return response.Body.String()
	}
	const row = "test@example.com:player:password:fake-token:1:device\n"
	upload("", row)
	body = upload("example game", row)
	if !strings.Contains(body, "В пуле 1 из 1000") || strings.Contains(body, "fake-token") {
		t.Fatal("pool count missing or upload secret exposed")
	}
	body = upload("other game", "test@example.com:new_player:password:fake-token:2:device\n"+row)
	if !strings.Contains(body, "уже закреплён за другой игрой") {
		t.Fatal("expected conflicting assignment to be rejected")
	}
	saved, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Accounts) != 1 || saved.Accounts[0].Game != "example game" {
		t.Fatal("expected atomic pool assignment")
	}
	opts := saved.Settings.MinerOptions
	if saved.Settings.MaxGames != 3 || saved.Settings.MaxAccounts != 1200 || !saved.Settings.SaveResources || opts == nil || !opts.SingleLog || opts.Diagnostics || opts.UsePubSub {
		t.Fatalf("unexpected saved settings: %+v, options: %+v", saved.Settings, opts)
	}
}
