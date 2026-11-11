package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var errTelegramMessageUnchanged = errors.New("telegram: сообщение уже актуально")

type SettingsButton struct {
	Label  string
	Action string
}

type SettingsPanel struct {
	Title       string
	Document    *TelegramDocument
	Input       string
	Text        string
	Buttons     [][]SettingsButton
	AfterRender func()
}

type telegramSettingsCallback struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message struct {
		ID   int64  `json:"message_id"`
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

func (b *Bot) ConfigureSettings(settings func(string) (SettingsPanel, bool)) { b.settingsFn = settings }

func (b *Bot) settingsRequest(ctx context.Context, method string, payload any) error {
	return b.settingsRequestResult(ctx, method, payload, nil)
}

func (b *Bot) settingsRequestResult(ctx context.Context, method string, payload any, output any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("telegram: неверный запрос")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: ошибка соединения")
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("telegram: запрос отклонён")
	}
	if method == "editMessageText" && resp.StatusCode == http.StatusBadRequest && strings.HasPrefix(result.Description, "Bad Request: message is not modified") {
		return errTelegramMessageUnchanged
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return fmt.Errorf("telegram: запрос отклонён")
	}
	if output != nil {
		return json.Unmarshal(result.Result, output)
	}
	return nil
}

func (b *Bot) handleSettings(ctx context.Context, action string) {
	if b.settingsFn == nil {
		b.sendContext(ctx, "Настройки недоступны.")
		return
	}
	panel, reload := b.settingsFn(action)
	keyboard := [][]map[string]string{}
	for _, row := range panel.Buttons {
		buttons := []map[string]string{}
		for _, button := range row {
			buttons = append(buttons, map[string]string{"text": button.Label, "callback_data": "settings:" + button.Action})
		}
		keyboard = append(keyboard, buttons)
	}
	_ = b.settingsRequest(ctx, "sendMessage", map[string]any{"chat_id": b.adminID, "text": truncate(panel.Text, 3500), "reply_markup": map[string]any{"inline_keyboard": keyboard}})
	if reload && b.reloadFn != nil {
		b.reloadFn()
	}
}

func isSettingsInteraction(i discordInteraction) bool {
	return i.Data.Name == "settings" || (i.Type == 3 && strings.HasPrefix(i.Data.CustomID, "settings:"))
}

func discordSettingsAction(i discordInteraction) string {
	if i.Type == 3 {
		return strings.TrimPrefix(i.Data.CustomID, "settings:")
	}
	for _, option := range i.Data.Options {
		if option.Name == "action" {
			var action string
			_ = json.Unmarshal(option.Value, &action)
			return action
		}
	}
	return ""
}

func (d *Discord) replySettings(ctx context.Context, i discordInteraction, panel SettingsPanel) error {
	rows := []map[string]any{}
	for _, row := range panel.Buttons {
		buttons := []map[string]any{}
		for _, button := range row {
			buttons = append(buttons, map[string]any{"type": 2, "style": 2, "label": button.Label, "custom_id": "settings:" + button.Action})
		}
		rows = append(rows, map[string]any{"type": 1, "components": buttons})
	}
	return d.api.request(ctx, "PATCH", "/webhooks/"+i.ApplicationID+"/"+i.Token+"/messages/@original", "reply", map[string]any{
		"content": truncate(panel.Text, 1900), "components": rows, "allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
}
