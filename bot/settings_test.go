package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTelegramSettingsButtonsRequireOwnerAndPrivateChat(t *testing.T) {
	calls := []string{}
	b := New("fixture", 1, nil, nil, nil, nil, nil, nil)
	b.ConfigureSettings(func(action string) (SettingsPanel, bool) {
		calls = append(calls, action)
		return SettingsPanel{Text: "Settings", Buttons: [][]SettingsButton{{{Label: "Apply", Action: "apply now"}}}}, false
	})
	b.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true}`
		if strings.Contains(r.URL.Path, "getUpdates") {
			body = `{"ok":true,"result":[
    {"update_id":1,"callback_query":{"id":"a","data":"settings:show","from":{"id":2},"message":{"chat":{"id":1,"type":"private"}}}},
    {"update_id":2,"callback_query":{"id":"b","data":"settings:show","from":{"id":1},"message":{"chat":{"id":-1,"type":"group"}}}},
    {"update_id":3,"callback_query":{"id":"c","data":"settings:show","from":{"id":1},"message":{"chat":{"id":1,"type":"private"}}}}
   ]}`
		}
		if strings.Contains(r.URL.Path, "sendMessage") {
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), "settings:apply now") {
				t.Error("missing inline controls")
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err := b.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "show" {
		t.Fatal("unauthorized callbacks executed", calls)
	}
}

func TestDiscordSettingsComponentsAndCommand(t *testing.T) {
	d := testDiscord()
	calls := 0
	d.actions.Settings = func(action string) (SettingsPanel, bool) {
		calls++
		return SettingsPanel{Text: action}, action == "apply now"
	}
	i := interaction("settings")
	raw, _ := json.Marshal(map[string]any{"name": "settings", "options": []map[string]any{{"name": "action", "value": "apply now"}}})
	json.Unmarshal(raw, &i.Data)
	text, reload := d.command(context.Background(), i)
	if text != "apply now" || !reload || calls != 1 {
		t.Fatal("slash settings failed")
	}
	i.Type = 3
	i.Data.CustomID = "settings:show"
	if !d.accept(i) || discordSettingsAction(i) != "show" {
		t.Fatal("owner component rejected")
	}
	i.User.ID = "999999999999999999"
	if d.accept(i) {
		t.Fatal("non-owner component accepted")
	}
}
