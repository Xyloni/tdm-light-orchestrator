package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
)

type Actions struct {
	Orchestrator func(string) string
	ExportLogs   func(context.Context, string) (name string, data []byte, message string)
	Interactive  func(context.Context, string, map[string]string, []byte) (string, []DiscordChoice)
	Settings     func(string) (SettingsPanel, bool)
	Status       func() string
	Logs         func(string) string
	Reload       func()
	Game         func() string
	SetGame      func(string) error
	Upload       func([]byte) error
	Excluded     func() []string
	SetExcluded  func(string, bool) error
}

type discordAttachment struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}
type discordInteraction struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Token         string `json:"token"`
	Type          int    `json:"type"`
	ChannelID     string `json:"channel_id"`
	GuildID       string `json:"guild_id"`
	Context       *int   `json:"context"`
	User          struct {
		ID string `json:"id"`
	} `json:"user"`
	Member struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"member"`
	Data struct {
		Name     string `json:"name"`
		CustomID string `json:"custom_id"`
		Options  []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"options"`
		Resolved struct {
			Attachments map[string]discordAttachment `json:"attachments"`
		} `json:"resolved"`
	} `json:"data"`
}

type queuedInteraction struct {
	item     discordInteraction
	received time.Time
}

type Discord struct {
	api          *discordAPI
	admin        string
	dmChannel    string
	dmGate       chan struct{}
	actions      Actions
	acks         chan queuedInteraction
	commands     chan discordInteraction
	registration chan string
	gatewayURL   string
	recent       [128]string
	recentNext   int
	choiceSets   map[string]discordChoiceSet
}

func NewDiscord(token, admin string, actions Actions) *Discord {
	return &Discord{
		api:   &discordAPI{base: "https://discord.com/api/v10", token: token, client: &http.Client{Timeout: 20 * time.Second}},
		admin: admin, dmGate: make(chan struct{}, 1), actions: actions,
		acks: make(chan queuedInteraction, 16), commands: make(chan discordInteraction, 8), registration: make(chan string, 1),
		gatewayURL: "wss://gateway.discord.gg/?v=10&encoding=json",
	}
}

func (d *Discord) Run(ctx context.Context) {
	defer d.api.client.CloseIdleConnections()
	var wg sync.WaitGroup
	for _, run := range []func(context.Context){d.acknowledge, d.execute, d.register} {
		wg.Add(1)
		go func(f func(context.Context)) { defer wg.Done(); f(ctx) }(run)
	}
	d.gateway(ctx)
	wg.Wait()
}

func (d *Discord) Send(ctx context.Context, text string) error {
	channel, err := d.directChannel(ctx)
	if err != nil {
		return err
	}
	return d.api.request(ctx, "POST", "/channels/"+channel+"/messages", "notify", map[string]any{
		"content": truncate(text, 1900), "allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
}

// Resolve the owner's DM once per manager lifetime. Never use a configured guild
// channel or an unverified interaction channel as a notification destination.
func (d *Discord) directChannel(ctx context.Context) (string, error) {
	select {
	case d.dmGate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-d.dmGate }()
	if d.dmChannel != "" {
		return d.dmChannel, nil
	}
	var channel struct {
		ID         string `json:"id"`
		Type       int    `json:"type"`
		Recipients []struct {
			ID string `json:"id"`
		} `json:"recipients"`
	}
	if err := d.api.request(ctx, "POST", "/users/@me/channels", "dm", map[string]string{"recipient_id": d.admin}, &channel); err != nil {
		return "", err
	}
	if !config.Snowflake(channel.ID) || channel.Type != 1 || len(channel.Recipients) != 1 || channel.Recipients[0].ID != d.admin {
		return "", fmt.Errorf("discord: invalid owner DM response")
	}
	d.dmChannel = channel.ID
	return d.dmChannel, nil
}

func (d *Discord) accept(i discordInteraction) bool {
	return (i.Type == 2 || (i.Type == 3 && (strings.HasPrefix(i.Data.CustomID, "settings:") || (d.actions.Interactive != nil && strings.HasPrefix(i.Data.CustomID, "control:"))))) && i.User.ID == d.admin && i.GuildID == "" && i.Member.User.ID == "" &&
		(i.Context == nil || *i.Context == 1) && config.Snowflake(i.ChannelID) &&
		config.Snowflake(i.ID) && config.Snowflake(i.ApplicationID) && i.Token != ""
}

func (d *Discord) enqueue(i discordInteraction) {
	if !d.accept(i) {
		return
	}
	// Called only by the single Gateway reader. Keep replay protection bounded.
	for _, id := range d.recent {
		if id == i.ID {
			return
		}
	}
	select {
	case d.acks <- queuedInteraction{i, time.Now()}:
		d.recent[d.recentNext] = i.ID
		d.recentNext = (d.recentNext + 1) % len(d.recent)
	default:
		// Drop excess control traffic without blocking Gateway heartbeats or farm.
	}
}

func (d *Discord) acknowledge(ctx context.Context) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case q := <-d.acks:
			// An expired interaction must never perform a destructive management action.
			deadline := q.received.Add(2500 * time.Millisecond)
			if time.Now().After(deadline) {
				continue
			}
			ackCtx, cancel := context.WithDeadline(ctx, deadline)
			err := d.api.request(ackCtx, "POST", "/interactions/"+q.item.ID+"/"+q.item.Token+"/callback", "ack", map[string]any{"type": 5, "data": map[string]any{"flags": 64}}, nil)
			cancel()
			if err != nil {
				log.Println("[discord] команда не подтверждена; повторите её")
				continue
			}
			select {
			case d.commands <- q.item:
			default:
				busyCtx, cancel := context.WithTimeout(ctx, time.Second)
				_ = d.reply(busyCtx, q.item, "Слишком много команд. Повторите позже.")
				cancel()
			}
		}
	}
}

func (d *Discord) reply(ctx context.Context, i discordInteraction, text string) error {
	return d.api.request(ctx, "PATCH", "/webhooks/"+i.ApplicationID+"/"+i.Token+"/messages/@original", "reply", map[string]any{
		"content": truncate(text, 1900), "allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
}

func (d *Discord) execute(ctx context.Context) {
	cleanup := time.NewTicker(15 * time.Second)
	defer cleanup.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case now := <-cleanup.C:
			d.expireChoices(now)
		case i := <-d.commands:
			commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			var text string
			var reload bool
			var replyErr error
			if i.Type == 2 && i.Data.Name == "download_logs" && d.actions.ExportLogs != nil {
				replyErr = d.replyLogArchive(commandCtx, i)
			} else if message, choices, handled := d.interactiveOrchestrator(commandCtx, i); handled {
				replyErr = d.replyChoices(commandCtx, i, message, choices)
			} else if isSettingsInteraction(i) && d.actions.Settings != nil {
				panel, changed := d.actions.Settings(discordSettingsAction(i))
				reload = changed
				replyErr = d.replySettings(commandCtx, i, panel)
			} else {
				text, reload = d.command(commandCtx, i)
				replyErr = d.reply(commandCtx, i, text)
			}
			if replyErr != nil && ctx.Err() == nil {
				log.Println("[discord] ответ на команду не доставлен")
			}
			cancel()
			if reload && ctx.Err() == nil && d.actions.Reload != nil {
				d.actions.Reload()
			}
		}
	}
}

func (d *Discord) command(ctx context.Context, i discordInteraction) (string, bool) {
	if ctx.Err() != nil {
		return "Команда отменена.", false
	}
	option := func(name string) string {
		for _, o := range i.Data.Options {
			if o.Name == name {
				var s string
				_ = json.Unmarshal(o.Value, &s)
				return s
			}
		}
		return ""
	}
	if d.actions.Orchestrator != nil {
		if spec, ok := findOrchestratorCommand(i.Data.Name); ok {
			return d.actions.Orchestrator(spec.input(option(spec.option))), false
		}
		if i.Data.Name != "accounts" {
			return d.actions.Orchestrator("help"), false
		}
	}
	switch i.Data.Name {
	case "settings":
		if d.actions.Settings == nil {
			return "Настройки недоступны.", false
		}
		panel, reload := d.actions.Settings(discordSettingsAction(i))
		return panel.Text, reload
	case "exclude":
		return exclusionCommand(option("name"), true, d.actions.Excluded, d.actions.SetExcluded)
	case "include":
		return exclusionCommand(option("name"), false, d.actions.Excluded, d.actions.SetExcluded)
	case "status":
		return d.actions.Status(), false
	case "logs":
		return d.actions.Logs(option("login")), false
	case "game":
		game := strings.TrimSpace(option("name"))
		if game == "" {
			return "Текущая игра: " + d.actions.Game(), false
		}
		if err := d.actions.SetGame(game); err != nil {
			return "Не удалось сохранить игру: " + err.Error(), false
		}
		return "Игра сохранена: " + game + ". Перезапускаю фарм.", true
	case "restart":
		return "Перезагружаю конфигурацию и account.txt…", true
	case "accounts":
		attachment, ok := i.Data.Resolved.Attachments[option("file")]
		if !ok {
			return "Прикрепите .txt файл к /accounts.", false
		}
		data, err := d.download(ctx, attachment)
		if err != nil {
			return err.Error(), false
		}
		if ctx.Err() != nil {
			return "Загрузка отменена.", false
		}
		if err = d.actions.Upload(data); err != nil {
			return "Файл не сохранён: " + err.Error(), false
		}
		if d.actions.Orchestrator != nil {
			return "Аккаунты сохранены.", false
		}
		return "account.txt проверен и сохранён. Перезапускаю фарм.", true
	default:
		return discordHelpText, false
	}
}

func (d *Discord) download(ctx context.Context, a discordAttachment) ([]byte, error) {
	if !strings.EqualFold(path.Ext(a.Filename), ".txt") || strings.ContainsAny(a.Filename, `/\`) || a.Size < 0 || a.Size > maxAccountsUploadBytes {
		return nil, fmt.Errorf("Нужен .txt файл размером до 2 МБ.")
	}
	u, err := url.Parse(a.URL)
	// Slash-command uploads use ephemeral-attachments rather than attachments.
	if err != nil || u.Scheme != "https" || (u.Host != "cdn.discordapp.com" && u.Host != "media.discordapp.net") ||
		(!strings.HasPrefix(u.Path, "/attachments/") && !strings.HasPrefix(u.Path, "/ephemeral-attachments/")) || u.User != nil {
		return nil, fmt.Errorf("Неверная ссылка на вложение Discord.")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("Неверная ссылка на файл.")
	}
	// CDN receives no bot token. Redirects cannot escape the attachment host allowlist.
	client := *d.api.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Не удалось скачать вложение.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Скачивание вложения: HTTP %d.", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAccountsUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("Ошибка чтения вложения.")
	}
	if len(data) > maxAccountsUploadBytes {
		return nil, fmt.Errorf("Файл больше 2 МБ.")
	}
	return data, nil
}

func (d *Discord) registeredCommands() []map[string]any {
	all := discordCommands()
	if d.actions.Orchestrator == nil {
		return all
	}
	selected := make([]map[string]any, 0, len(orchestratorCommands)+1)
	if d.actions.ExportLogs != nil {
		selected = append(selected, map[string]any{
			"name": "download_logs", "description": "Скачать ZIP с логами за выбранный день",
			"type": 1, "contexts": []int{1}, "integration_types": []int{0},
			"options": []map[string]any{{"type": 3, "name": "date", "description": "ГГГГ-ММ-ДД по времени сервера. Пустое поле означает сегодня.", "max_length": 10}},
		})
	}
	for _, spec := range orchestratorCommands {
		if d.actions.Interactive != nil && spec.name == "remove_game" {
			continue
		}
		cmd := map[string]any{"name": spec.name, "description": spec.description, "type": 1, "contexts": []int{1}, "integration_types": []int{0}}
		if spec.option != "" {
			cmd["options"] = []map[string]any{{"type": 3, "name": spec.option, "description": spec.hint, "required": spec.required, "max_length": 500}}
		}
		selected = append(selected, cmd)
	}
	if d.actions.Interactive != nil {
		return append(selected, orchestratorDiscordCommands()...)
	}
	for _, cmd := range all {
		switch cmd["name"] {
		case "accounts":
			selected = append(selected, cmd)
		}
	}
	return selected
}

const discordHelpText = `Справка самостоятельного майнера TDM Light

/status — текущая игра, группа, прогресс и готовые награды.
/logs → login: my_login — последние записи выбранного аккаунта.
/game — показать текущую игру. Укажите name: Example Game, чтобы сохранить новую игру и перезапустить фарм.
/settings — открыть настройки автовыключения, диагностики и логов. «Применить сейчас» перезапускает обработку групп. «После группы» ждёт завершения активной группы.
/exclude — показать исключения. Укажите name: Example Reward, чтобы исключить награду по полному имени.
/include → name: Example Reward — вернуть награду в список целей.
/restart — перечитать конфигурацию и account.txt и перезапустить фарм.
/accounts → file: accounts.txt — загрузить файл до 2 МБ. После проверки файл заменит account.txt и майнер перечитает группы.

Команды доступны владельцу в личке бота. Награды получаются вручную в Twitch.`

func discordCommands() []map[string]any {
	commands := []map[string]any{}
	for _, c := range []struct {
		name, description string
		options           []map[string]any
	}{
		{"settings", "Settings / Настройки", []map[string]any{{"type": 3, "name": "action", "description": "Optional: KEY VALUE or apply now / apply after", "max_length": 100}}},
		{"help", "Show commands / Команды", nil}, {"status", "Farm status / Статус фарма", nil},
		{"logs", "Account logs / Логи аккаунта", []map[string]any{{"type": 3, "name": "login", "description": "Account login", "required": true}}},
		{"game", "Show or change target game / Целевая игра", []map[string]any{{"type": 3, "name": "name", "description": "Target game", "max_length": 200}}},
		{"restart", "Reload config and accounts / Перезапуск", nil},
		{"accounts", "Upload a .txt account file / Загрузка аккаунтов", []map[string]any{{"type": 11, "name": "file", "description": "Any .txt file, maximum 2 MiB", "required": true}}},
		{"exclude", "Show or add excluded reward / Исключить награду", []map[string]any{{"type": 3, "name": "name", "description": "Full reward name", "max_length": 200}}},
		{"include", "Remove excluded reward / Вернуть награду", []map[string]any{{"type": 3, "name": "name", "description": "Full reward name", "max_length": 200, "required": true}}},
	} {
		command := map[string]any{"name": c.name, "description": c.description, "type": 1, "contexts": []int{1}, "integration_types": []int{0}}
		if len(c.options) > 0 {
			command["options"] = c.options
		}
		commands = append(commands, command)
	}
	return commands
}

func (d *Discord) register(ctx context.Context) {
	for ctx.Err() == nil {
		var app string
		select {
		case <-ctx.Done():
			return
		case app = <-d.registration:
		}
		for ctx.Err() == nil {
			attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := d.api.request(attempt, "PUT", "/applications/"+app+"/commands", "register", d.registeredCommands(), nil)
			cancel()
			if err == nil {

				log.Println("[discord] команды зарегистрированы")
				break
			}
			log.Printf("[discord] регистрация команд не удалась: %v; повтор через минуту", err)
			if !sleepCtx(ctx, time.Minute) {
				return
			}
		}
	}
}
