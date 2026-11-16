package bot

import (
	"context"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"
)

func TestTelegramLotLinkUsesPrivateOwnerRoute(t *testing.T) {
	b := New("fixture", 123, nil, nil, nil, nil, nil, nil)
	var actions []string
	b.ConfigureOrchestratorMenu(func(action string) SettingsPanel {
		actions = append(actions, action)
		return SettingsPanel{Text: "Test"}
	}, nil)
	b.http = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		reply := `{"ok":true,"result":{"message_id":20}}`
		if path.Base(r.URL.Path) == "getUpdates" {
			reply = `{"ok":true,"result":[
				{"update_id":1,"message":{"text":"/start lot_0123456789abcdef01234567","chat":{"id":123,"type":"group"},"from":{"id":123}}},
				{"update_id":2,"message":{"text":"/start lot_0123456789abcdef01234567","chat":{"id":123,"type":"private"},"from":{"id":321}}},
				{"update_id":3,"message":{"text":"/start lot_0123456789abcdef01234567","chat":{"id":123,"type":"private"},"from":{"id":123}}},
				{"update_id":4,"message":{"text":"/start lot_invalid","chat":{"id":123,"type":"private"},"from":{"id":123}}}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}
	if err := b.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"lb-start:0123456789abcdef01234567", "menu"}; !slices.Equal(actions, want) {
		t.Fatalf("actions=%v, want %v", actions, want)
	}
}

func TestTelegramLotLinkCachesPublicUsername(t *testing.T) {
	for _, test := range []struct{ telegramValue, want string }{
		{telegramValue: "DropsManagerBot", want: "DropsManagerBot"},
		{telegramValue: "invalid?start=foo"},
	} {
		b := New("fixture", 123, nil, nil, nil, nil, nil, nil)
		calls := 0
		b.http = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
			if path.Base(r.URL.Path) != "getMe" {
				t.Errorf("unexpected Telegram method %q", path.Base(r.URL.Path))
			}
			calls++
			reply := `{"ok":true,"result":{"username":"` + test.telegramValue + `"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply))}, nil
		})}
		for i := 0; i < 2; i++ {
			if got := b.Username(context.Background()); got != test.want {
				t.Fatalf("username=%q, want %q", got, test.want)
			}
		}
		if calls != 1 {
			t.Fatalf("made %d getMe requests, want one", calls)
		}
	}
}
