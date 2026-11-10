package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

const maxAccountsUploadBytes = 2 << 20

type telegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

// PollState survives manager reloads. A process owns one state for its bot.
type PollState struct {
	offset atomic.Int64
}

// Bot — минимальный Telegram-бот на long polling.
// Команды принимаются только от администратора (adminID).
type Bot struct {
	orchestratorFn        func(string) string
	orchestratorMenu      func(string) SettingsPanel
	menuInput             string
	menuInputUntil        time.Time
	uploadToGame          func(string, []byte) error
	menuConfirmations     map[string]bool
	menuConfirmationUntil time.Time
	menuMessageID         int64
	latestChatMessage     atomic.Int64
	saveMenuMessage       func(int64)
	settingsFn            func(string) (SettingsPanel, bool)
	token                 string
	adminID               int64
	http                  *http.Client
	pollState             *PollState

	statusFn      func() string
	logsFn        func(login string) string
	reloadFn      func()
	gameFn        func() string
	setGame       func(string) error
	uploadFn      func([]byte) error
	excludedFn    func() []string
	setExcludedFn func(string, bool) error
}

func (b *Bot) ConfigureOrchestrator(command func(string) string) {
	b.orchestratorFn = command
}

func (b *Bot) ConfigureExclusions(get func() []string, set func(string, bool) error) {
	b.excludedFn = get
	b.setExcludedFn = set
}

func New(token string, adminID int64, statusFn func() string, logsFn func(string) string, reloadFn func(), gameFn func() string, setGame func(string) error, uploadFn func([]byte) error, states ...*PollState) *Bot {
	state := new(PollState)
	if len(states) > 0 && states[0] != nil {
		state = states[0]
	}
	return &Bot{
		pollState: state,
		token:     token,
		adminID:   adminID,
		http:      &http.Client{Timeout: 40 * time.Second},
		statusFn:  statusFn,
		logsFn:    logsFn,
		reloadFn:  reloadFn,
		gameFn:    gameFn,
		setGame:   setGame,
		uploadFn:  uploadFn,
	}
}

// Run крутит long polling до отмены ctx.
func (b *Bot) Run(ctx context.Context) {
	log.Println("[bot] long polling запущен")
	if b.orchestratorMenu != nil {
		b.registerOrchestratorMenu(ctx)
	}
	for ctx.Err() == nil {
		if err := b.poll(ctx); err != nil && ctx.Err() == nil {
			log.Println("[bot] getUpdates: ошибка соединения или ответа Telegram; повтор через 5 секунд")
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
		}
	}
}

func (b *Bot) poll(ctx context.Context) error {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=25&allowed_updates=%%5B%%22message%%22,%%22callback_query%%22%%5D",
		b.token, b.pollState.offset.Load())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var res struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64                     `json:"update_id"`
			Callback *telegramSettingsCallback `json:"callback_query"`
			Message  *struct {
				ID       int64             `json:"message_id"`
				Text     string            `json:"text"`
				Document *telegramDocument `json:"document"`
				Chat     struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				From struct {
					ID int64 `json:"id"`
				} `json:"from"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("telegram: ok=false")
	}
	for _, upd := range res.Result {
		if err := ctx.Err(); err != nil {
			return err
		}
		if upd.UpdateID < b.pollState.offset.Load() {
			continue
		}
		b.pollState.offset.Store(upd.UpdateID + 1)
		if callback := upd.Callback; callback != nil {
			if callback.From.ID == b.adminID && callback.Message.Chat.ID == b.adminID && callback.Message.Chat.Type == "private" {
				switch {
				case b.orchestratorMenu != nil && strings.HasPrefix(callback.Data, "control:"):
					if b.settingsRequest(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback.ID}) == nil {
						var menuID int64
						if callback.Message.Text != "" {
							menuID = callback.Message.ID
						}
						b.handleOrchestratorButton(ctx, strings.TrimPrefix(callback.Data, "control:"), menuID)
					}
				case strings.HasPrefix(callback.Data, "settings:"):
					if b.settingsRequest(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback.ID}) == nil {
						b.handleSettings(ctx, strings.TrimPrefix(callback.Data, "settings:"))
					}
				}
			}
			continue
		}
		msg := upd.Message
		if msg == nil || msg.From.ID != b.adminID {
			continue
		}
		if b.orchestratorMenu != nil && (msg.Chat.ID != b.adminID || msg.Chat.Type != "private") {
			continue
		}
		if b.orchestratorMenu != nil {
			b.observeChatMessage(msg.ID)
		}
		b.handleMessage(ctx, msg.Text, msg.Document, msg.ID)
	}
	return nil
}

func (b *Bot) handle(ctx context.Context, text string, document *telegramDocument) {
	b.handleMessage(ctx, text, document, 0)
}

func (b *Bot) handleMessage(ctx context.Context, text string, document *telegramDocument, messageID int64) {
	if document != nil {
		b.handleAccountsDocument(ctx, document.FileID, document.FileName, document.FileSize)
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	if b.orchestratorMenu != nil {
		b.handleOrchestratorText(ctx, cmd, strings.TrimSpace(text[len(fields[0]):]), strings.TrimSpace(text), messageID)
		return
	}
	if b.orchestratorFn != nil {
		if spec, ok := findOrchestratorCommand(strings.TrimPrefix(cmd, "/")); ok {
			b.sendContext(ctx, b.orchestratorFn(spec.input(text[len(fields[0]):])))
		} else {
			b.sendContext(ctx, b.orchestratorFn("help"))
		}
		return
	}
	switch cmd {
	case "/settings":
		b.handleSettings(ctx, strings.TrimSpace(text[len(fields[0]):]))
	case "/exclude", "/include":
		name := strings.TrimSpace(text[len(fields[0]):])
		response, reload := exclusionCommand(name, cmd == "/exclude", b.excludedFn, b.setExcludedFn)
		b.sendContext(ctx, response)
		if reload && b.reloadFn != nil {
			b.reloadFn()
		}
	case "/help", "/start":
		b.sendContext(ctx, helpText)
	case "/status":
		b.sendContext(ctx, b.statusFn())
	case "/logs":
		if len(fields) < 2 {
			b.sendContext(ctx, "Использование: /logs <login>")
			return
		}
		b.sendContext(ctx, b.logsFn(fields[1]))
	case "/game":
		game := ""
		if len(fields) > 1 {
			game = strings.TrimSpace(text[len(fields[0]):])
		}
		if game == "" {
			current := "не задана"
			if b.gameFn != nil {
				if configured := b.gameFn(); configured != "" {
					current = configured
				}
			}
			b.sendContext(ctx, "Текущая игра: "+current+"\nИзменить: /game Название игры")
			return
		}
		if b.setGame == nil {
			b.sendContext(ctx, "Изменение игры не настроено.")
			return
		}
		if err := b.setGame(game); err != nil {
			b.sendContext(ctx, "Не удалось сохранить игру: "+err.Error())
			return
		}
		b.sendContext(ctx, "🎮 Игра сохранена: "+game+"\nПерезапускаю майнинг с новой игрой.")
		if b.reloadFn != nil {
			b.reloadFn()
		}
	case "/restart":
		b.sendContext(ctx, "♻️ Перезагружаю config.json и account.txt...")
		b.reloadFn()
	default:
		b.sendContext(ctx, helpText)
	}
}

const helpText = `Справка самостоятельного майнера TDM Light

/status — текущая игра, активная группа, прогресс и готовые награды.
/logs my_login — последние записи аккаунта my_login, включая смены каналов и ошибки.
/game — показать текущую игру. Пример смены: /game Example Game. Новая игра сохраняется, текущий фарм перезапускается.
/settings — показать настройки автовыключения, диагностики и логов. «Применить сейчас» перезапускает обработку групп. «После группы» ждёт её завершения.
/exclude — показать список исключений. Пример: /exclude Example Reward добавляет награду по полному имени.
/include Example Reward — возвращает награду в список целей.
/restart — перечитать настройки и account.txt и перезапустить фарм.

Загрузка: отправьте файл .txt до 2 МБ документом. После проверки файл заменит account.txt и майнер перечитает группы. Награды получаются вручную в Twitch.`

func (b *Bot) handleAccountsDocument(ctx context.Context, fileID, fileName string, fileSize int64) {
	reply := func(text string) {
		if b.orchestratorMenu != nil {
			panel := orchestratorReply(text)
			if strings.HasPrefix(b.menuInput, "upload:") && time.Now().Before(b.menuInputUntil) {
				panel.Input = b.menuInput
			}
			b.renderOrchestratorPanel(ctx, "accounts", panel, 0, true)
			return
		}
		b.sendContext(ctx, text)
	}
	if !strings.EqualFold(path.Ext(fileName), ".txt") || strings.ContainsAny(fileName, `/\\`) {
		reply("Пришли документ с любым названием и расширением .txt.")
		return
	}
	if b.uploadFn == nil {
		reply("Загрузка account.txt не настроена.")
		return
	}
	if fileSize > maxAccountsUploadBytes {
		reply("Файл слишком большой (максимум 2 МБ).")
		return
	}
	content, err := b.downloadDocument(ctx, fileID)
	if err != nil {
		reply("Не удалось скачать файл из Telegram: " + err.Error())
		return
	}
	upload := b.uploadFn
	if ref, ok := strings.CutPrefix(b.menuInput, "upload:"); ok && b.uploadToGame != nil {
		if !time.Now().Before(b.menuInputUntil) {
			reply("Срок выбора пула истёк. Откройте игру и выберите загрузку заново.")
			return
		}
		upload = func(data []byte) error { return b.uploadToGame(ref, data) }
	}
	if err := upload(content); err != nil {
		reply("Файл не сохранён: " + err.Error())
		return
	}
	if b.orchestratorFn != nil || b.orchestratorMenu != nil {
		b.menuInput = ""
		reply("Аккаунты сохранены.")
		return
	}
	b.sendContext(ctx, "✅ account.txt проверен и сохранён. Перезапускаю фарм с новым списком аккаунтов.")
	if b.reloadFn != nil {
		b.reloadFn()
	}
}

func (b *Bot) downloadDocument(ctx context.Context, fileID string) ([]byte, error) {
	payload, _ := json.Marshal(map[string]string{"file_id": fileID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/getFile", b.token), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("getFile вернул HTTP %d", resp.StatusCode)
	}
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK || result.Result.FilePath == "" {
		return nil, fmt.Errorf("Telegram не вернул путь к файлу")
	}
	cleanPath := path.Clean(result.Result.FilePath)
	if cleanPath == "." || strings.HasPrefix(cleanPath, "../") || strings.HasPrefix(cleanPath, "/") {
		return nil, fmt.Errorf("Telegram вернул некорректный путь к файлу")
	}
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.token, cleanPath)
	fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	fileResp, err := b.http.Do(fileReq)
	if err != nil {
		return nil, err
	}
	defer fileResp.Body.Close()
	if fileResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание вернуло HTTP %d", fileResp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(fileResp.Body, maxAccountsUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxAccountsUploadBytes {
		return nil, fmt.Errorf("файл слишком большой (максимум 2 МБ)")
	}
	return content, nil
}

func (b *Bot) sendContext(parent context.Context, text string) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := b.Send(ctx, text); err != nil && parent.Err() == nil {
		log.Println("[bot] sendMessage: ошибка доставки")
	}
}

func (b *Bot) Send(ctx context.Context, text string) error {
	text = truncate(text, 4000)
	payload, _ := json.Marshal(map[string]any{
		"chat_id": b.adminID,
		"text":    text,
	})
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: неверный запрос")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: ошибка соединения")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	if b.orchestratorMenu != nil {
		var response struct {
			Result struct {
				ID int64 `json:"message_id"`
			} `json:"result"`
		}
		if json.NewDecoder(resp.Body).Decode(&response) == nil {
			b.observeChatMessage(response.Result.ID)
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
