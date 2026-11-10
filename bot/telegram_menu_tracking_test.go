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
	"time"
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
		b.renderOrchestratorMenu(context.Background(), "menu", 0)
		if !reflect.DeepEqual(calls, test.calls) || tracked != test.wantID {
			t.Fatalf("calls=%v id=%d", calls, tracked)
		}
	}
}

func TestTelegramCleanupKeepsFilesAndMovesBuriedMenu(t *testing.T) {
	b := New("fake", 123, nil, nil, nil, nil, nil, nil)
	b.TrackOrchestratorMenu(41, nil)
	b.ConfigureOrchestratorMenu(func(action string) SettingsPanel {
		if action == "logs:" {
			return SettingsPanel{Document: &TelegramDocument{Name: "logs.zip", Data: []byte("fixture")}}
		}
		return SettingsPanel{Text: "Настройки"}
	}, nil)
	b.menuInput, b.menuInputUntil = "limits", time.Now().Add(time.Minute)
	var deleted []int64
	var menuID int64 = 100
	allowSend := false
	b.http = &http.Client{Transport: documentTransport(func(r *http.Request) (*http.Response, error) {
		reply := `{"ok":true}`
		switch path.Base(r.URL.Path) {
		case "sendMessage":
			if allowSend {
				menuID++
				data, err := json.Marshal(map[string]any{"ok": true, "result": map[string]int64{"message_id": menuID}})
				if err != nil {
					t.Fatal(err)
				}
				reply = string(data)
			} else {
				reply = `{"ok":false}`
			}
		case "deleteMessage":
			var payload struct {
				ID int64 `json:"message_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			deleted = append(deleted, payload.ID)
		case "sendDocument":
			reply = `{"ok":true,"result":{"message_id":95}}`
		case "editMessageText":
			t.Fatal("an older panel should be replaced at the bottom of the chat")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}

	ctx := context.Background()
	b.handleMessage(ctx, "2 1000", nil, 50)
	if len(deleted) != 0 {
		t.Fatalf("deleted messages after a failed render: %v", deleted)
	}
	allowSend = true
	b.handleMessage(ctx, "2 1000", nil, 51)
	b.handleMessage(ctx, "обычная переписка", nil, 55)
	b.handleMessage(ctx, "", &telegramDocument{FileName: "photo.png"}, 60)
	b.handleMessage(ctx, "/download_logs", nil, 90)
	b.observeChatMessage(105) // A later file or notification has buried the panel.
	b.handleOrchestratorButton(ctx, "menu", 103)
	if want := []int64{41, 51, 101, 102, 90, 103}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted %v, want only obsolete panels and processed text %v", deleted, want)
	}
}
