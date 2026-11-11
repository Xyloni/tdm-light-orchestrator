package bot

import (
	"context"
	"errors"
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
		{"command": "lagging", "description": "Темп начисления за 10–15 и 30–35 минут"},
		{"command": "deadline", "description": "Отчёты по ближайшим дедлайнам кампаний"},
		{"command": "system_load", "description": "Нагрузка VPS и контейнера"},
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
		b.renderOrchestratorPanel(ctx, action, orchestratorReply("Подтверждение устарело. Откройте действие заново."), messageID, false)
		return
	}
	b.renderOrchestratorMenu(ctx, action, messageID)
}

func (b *Bot) renderOrchestratorMenu(ctx context.Context, action string, messageID int64) {
	b.renderOrchestratorPanel(ctx, action, b.orchestratorMenu(action), messageID, false)
}

func orchestratorReply(text string) SettingsPanel {
	return SettingsPanel{Title: "💬 Ответ оркестратора", Text: text, Buttons: [][]SettingsButton{{{Label: "В меню", Action: "menu"}}}}
}

func (b *Bot) handleOrchestratorText(ctx context.Context, cmd, args, text string, messageID int64) {
	var action string
	var panel SettingsPanel
	switch {
	case !strings.HasPrefix(cmd, "/"):
		if b.menuInput == "" || strings.HasPrefix(b.menuInput, "upload:") || !time.Now().Before(b.menuInputUntil) {
			return
		}
		action = b.menuInput + "\n" + text
	case cmd == "/start":
		b.registerOrchestratorMenu(ctx)
		action = "menu"
	case cmd == "/menu" || cmd == "/games" || cmd == "/settings" || cmd == "/status":
		action = strings.TrimPrefix(cmd, "/")
	case cmd == "/download_logs":
		action = "logs:" + args
	case cmd == "/help":
		action = "help:" + args
	case cmd == "/deadline":
		action = "deadline"
	case cmd == "/system_load":
		action = "system-load"
	case args == "" && (cmd == "/notifications" || cmd == "/lagging" || cmd == "/stats"):
		action = strings.TrimPrefix(cmd, "/")
	default:
		spec, ok := findOrchestratorCommand(strings.TrimPrefix(cmd, "/"))
		if !ok || b.orchestratorFn == nil {
			return
		}
		panel = orchestratorReply(b.orchestratorFn(spec.input(args)))
	}
	if action != "" {
		panel = b.orchestratorMenu(action)
	}
	if b.renderOrchestratorPanel(ctx, action, panel, 0, messageID > 0) && messageID > 0 {
		b.deleteOrchestratorText(ctx, messageID)
	}
}

func (b *Bot) renderOrchestratorPanel(ctx context.Context, action string, panel SettingsPanel, fallbackID int64, moveToBottom bool) (rendered bool) {
	if panel.AfterRender != nil {
		defer func() {
			if rendered {
				panel.AfterRender()
			}
		}()
	}
	if panel.Document != nil {
		panel.Text = b.sendOrchestratorDocument(ctx, panel.Document)
		moveToBottom = true
	}
	confirmations := make(map[string]bool)
	keyboard := make([][]map[string]string, 0, len(panel.Buttons))
	for _, row := range panel.Buttons {
		buttons := make([]map[string]string, 0, len(row))
		for _, button := range row {
			data := "control:" + button.Action
			if len(data) <= 64 {
				if confirmationAction(button.Action) {
					confirmations[button.Action] = true
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
		previous = fallbackID
	}
	moveToBottom = moveToBottom || b.latestChatMessage.Load() > previous
	remember := func(id int64) {
		b.menuConfirmations = confirmations
		b.menuConfirmationUntil = time.Now().Add(2 * time.Minute)
		b.menuInput = panel.Input
		b.menuInputUntil = time.Now().Add(5 * time.Minute)
		b.rememberMenuMessage(id)
	}
	if previous > 0 && !moveToBottom {
		payload["message_id"] = previous
		if err := b.settingsRequest(ctx, "editMessageText", payload); err == nil || errors.Is(err, errTelegramMessageUnchanged) {
			remember(previous)
			return true
		}
		delete(payload, "message_id")
	}
	var sent struct {
		ID int64 `json:"message_id"`
	}
	if err := b.settingsRequestResult(ctx, "sendMessage", payload, &sent); err != nil || sent.ID <= 0 {
		log.Println("[bot] не удалось отправить панель оркестратора")
		return false
	}
	remember(sent.ID)
	if previous > 0 && previous != sent.ID {
		b.deleteOrchestratorText(ctx, previous)
	}
	return true
}

func (b *Bot) deleteOrchestratorText(parent context.Context, messageID int64) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := b.settingsRequest(ctx, "deleteMessage", map[string]any{"chat_id": b.adminID, "message_id": messageID}); err != nil && parent.Err() == nil {
		log.Println("[bot] Telegram не удалил служебное сообщение")
	}
}

func (b *Bot) rememberMenuMessage(id int64) {
	b.observeChatMessage(id)
	if b.menuMessageID == id {
		return
	}
	b.menuMessageID = id
	if b.saveMenuMessage != nil {
		b.saveMenuMessage(id)
	}
}

func (b *Bot) observeChatMessage(id int64) {
	for previous := b.latestChatMessage.Load(); id > previous; previous = b.latestChatMessage.Load() {
		if b.latestChatMessage.CompareAndSwap(previous, id) {
			return
		}
	}
}
