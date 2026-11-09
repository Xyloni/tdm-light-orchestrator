package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"reflect"
	"strings"
	"testing"
)

func TestTelegramMenuDeletesPreviousOnlyAfterReplacement(t *testing.T) {
	for _, test := range []struct {
		edit, send bool
		calls      []string
		wantID     int64
	}{
		{true, true, []string{"editMessageText"}, 41},
		{false, false, []string{"editMessageText", "sendMessage"}, 41},
		{false, true, []string{"editMessageText", "sendMessage", "deleteMessage"}, 42},
	} {
		b := New("fake", 123, nil, nil, nil, nil, nil, nil)
		var calls []string
		tracked := int64(41)
		b.TrackOrchestratorMenu(tracked, func(id int64) { tracked = id })
		b.ConfigureOrchestratorMenu(func(string) SettingsPanel { return SettingsPanel{Text: "Test"} }, nil)
		b.http = &http.Client{Transport: documentTransport(func(r *http.Request) (*http.Response, error) {
			method := path.Base(r.URL.Path)
			calls = append(calls, method)
			var data map[string]any
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Fatal(err)
			}
			if data["chat_id"] != float64(123) {
				t.Fatal("menu request was sent to another chat")
			}
			reply := `{"ok":false}`
			switch method {
			case "editMessageText":
				if test.edit {
					reply = `{"ok":true}`
				}
			case "sendMessage":
				if test.send {
					reply = `{"ok":true,"result":{"message_id":42}}`
				}
			case "deleteMessage":
				if data["message_id"] != float64(41) || tracked != 42 {
					t.Fatal("deleted the wrong menu or deleted before recording the replacement")
				}
				reply = `{"ok":true}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
		})}
		b.showOrchestratorMenu(context.Background(), "menu")
		if !reflect.DeepEqual(calls, test.calls) || tracked != test.wantID {
			t.Fatalf("calls=%v id=%d", calls, tracked)
		}
	}
}
