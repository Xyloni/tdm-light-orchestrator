package bot

import (
	"context"
	"log"
	"strings"
	"time"
)

func (b *Bot) ConfigureOrchestratorMenu(menu func(string) SettingsPanel, upload func(string, []byte) error) {
	b.orchestratorMenu = menu
	b.uploadToGame = upload
}

func (b *Bot) TrackOrchestratorMenu(messageID int64, save func(int64)) {
	b.menuMessageID = messageID
	b.saveMenuMessage = save
}

func (b *Bot) registerOrchestratorMenu(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	commands := []map[string]string{
		{"command": "menu", "description": "Панель управления с кнопками"},
		{"command": "status", "description": "Активные кампании и состояние Twitch"},
		{"command": "lagging", "description": "Отстающие аккаунты и скорость начисления"},
		{"command": "stats", "description": "Статистика за сегодня, неделю и всё время"},
		{"command": "download_logs", "description": "Скачать архив дневных логов"},
		{"command": "games", "description": "Игры, пауза и перезапуск"},
		{"command": "settings", "description": "Режим группировки и снижение нагрузки"},
		{"command": "help", "description": "Справка и примеры"},
		{"command": "notifications", "description": "Переключатели уведомлений"},
	}
	if err := b.settingsRequest(ctx, "setMyCommands", map[string]any{
		"scope": map[string]any{"type": "chat", "chat_id": b.adminID}, "commands": commands,
	}); err != nil {
		log.Println("[bot] меню команд не зарегистрировано. Откройте личку бота и отправьте /start.")
		return
	}
	if err := b.settingsRequest(ctx, "setChatMenuButton", map[string]any{
		"chat_id": b.adminID, "menu_button": map[string]string{"type": "commands"},
	}); err != nil {
		log.Println("[bot] не удалось обновить кнопку меню Telegram")
	}
}

func confirmationAction(action string) bool {
	kind, _, _ := strings.Cut(action, ":")
	return strings.HasSuffix(kind, "-confirm")
}

func (b *Bot) handleOrchestratorButton(ctx context.Context, action string, messageID int64) {
	if confirmationAction(action) && (!b.menuConfirmations[action] || !time.Now().Before(b.menuConfirmationUntil)) {
		b.sendContext(ctx, "Подтверждение устарело. Откройте действие заново.")
		return
	}
	b.renderOrchestratorMenu(ctx, action, messageID)
}

func (b *Bot) showOrchestratorMenu(ctx context.Context, action string) {
	b.renderOrchestratorMenu(ctx, action, 0)
}

func (b *Bot) renderOrchestratorMenu(ctx context.Context, action string, messageID int64) {
	b.menuConfirmations = make(map[string]bool)
	panel := b.orchestratorMenu(action)
	if panel.Document != nil {
		panel.Text = b.sendOrchestratorDocument(ctx, panel.Document)
	}
	b.menuInput = panel.Input
	b.menuInputUntil = time.Now().Add(5 * time.Minute)
	keyboard := make([][]map[string]string, 0, len(panel.Buttons))
	for _, row := range panel.Buttons {
		buttons := make([]map[string]string, 0, len(row))
		for _, button := range row {
			data := "control:" + button.Action
			if len(data) <= 64 {
				if confirmationAction(button.Action) {
					b.menuConfirmations[button.Action] = true
					b.menuConfirmationUntil = time.Now().Add(2 * time.Minute)
				}
				buttons = append(buttons, map[string]string{"text": telegramButton(button.Action, button.Label), "callback_data": data})
			}
		}
		if len(buttons) > 0 {
			keyboard = append(keyboard, buttons)
		}
	}
	payload := map[string]any{
		"chat_id": b.adminID, "text": formatTelegramPanel(action, panel.Title, panel.Text), "parse_mode": "HTML",
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	}
	previous := b.menuMessageID
	if previous == 0 {
		previous = messageID
	}
	if previous > 0 {
		payload["message_id"] = previous
		if b.settingsRequest(ctx, "editMessageText", payload) == nil {
			b.rememberMenuMessage(previous)
			return
		}
		delete(payload, "message_id")
	}
	var sent struct {
		ID int64 `json:"message_id"`
	}
	if err := b.settingsRequestResult(ctx, "sendMessage", payload, &sent); err != nil || sent.ID <= 0 {
		log.Println("[bot] не удалось отправить панель оркестратора")
		return
	}
	b.rememberMenuMessage(sent.ID)
	if previous > 0 && previous != sent.ID {
		if err := b.settingsRequest(ctx, "deleteMessage", map[string]any{"chat_id": b.adminID, "message_id": previous}); err != nil {
			log.Println("[bot] Telegram не удалил предыдущее меню")
		}
	}
}

func (b *Bot) rememberMenuMessage(id int64) {
	if b.menuMessageID == id {
		return
	}
	b.menuMessageID = id
	if b.saveMenuMessage != nil {
		b.saveMenuMessage(id)
	}
}
