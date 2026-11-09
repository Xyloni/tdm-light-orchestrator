package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type DiscordChoice struct {
	Label string
	Run   func() string
}

type discordChoiceSet struct {
	expires time.Time
	actions map[string]func() string
}

func orchestratorDiscordCommands() []map[string]any {
	text := func(name, description string, required bool) map[string]any {
		return map[string]any{"type": 3, "name": name, "description": description, "required": required, "max_length": 200}
	}
	command := func(name, description string, options ...map[string]any) map[string]any {
		return map[string]any{"name": name, "description": description, "type": 1, "contexts": []int{1}, "integration_types": []int{0}, "options": options}
	}
	return []map[string]any{
		command("find_game", "Найти категорию Twitch и добавить игру", text("query", "Название или часть названия Twitch", true), text("target", "Целевой размер пула аккаунтов", true)),
		command("accounts", "Проверить .txt и загрузить аккаунты после подтверждения", map[string]any{"type": 11, "name": "file", "description": "Файл .txt до 2 МБ", "required": true}, text("game", "Название игры, пустое поле означает свободный пул", false)),
		command("clear_pool", "Очистить выбранный пул после подтверждения", text("pool", "Название игры или free для свободного пула", true)),
		command("remove_game", "Удалить игру и выбрать судьбу её аккаунтов", text("game", "Полное название игры", true)),
	}
}

func (d *Discord) interactiveOrchestrator(ctx context.Context, i discordInteraction) (string, []DiscordChoice, bool) {
	if d.actions.Interactive == nil {
		return "", nil, false
	}
	if i.Type == 3 {
		action, ok := strings.CutPrefix(i.Data.CustomID, "control:")
		if !ok {
			return "", nil, false
		}
		id, _, _ := strings.Cut(action, ":")
		set, exists := d.choiceSets[id]
		run := set.actions[action]
		if !exists || time.Now().After(set.expires) || run == nil {
			return "Подтверждение устарело. Запустите команду заново.", nil, true
		}
		delete(d.choiceSets, id)
		return run(), nil, true
	}
	switch i.Data.Name {
	case "find_game", "accounts", "clear_pool", "remove_game":
	default:
		return "", nil, false
	}
	options := make(map[string]string)
	for _, o := range i.Data.Options {
		var value string
		if json.Unmarshal(o.Value, &value) == nil {
			options[o.Name] = value
		}
	}
	var data []byte
	if i.Data.Name == "accounts" {
		attachment, ok := i.Data.Resolved.Attachments[options["file"]]
		if !ok {
			return "Прикрепите .txt файл к /accounts.", nil, true
		}
		var err error
		data, err = d.download(ctx, attachment)
		if err != nil {
			return err.Error(), nil, true
		}
	}
	message, choices := d.actions.Interactive(ctx, i.Data.Name, options, data)
	return message, choices, true
}

func (d *Discord) expireChoices(now time.Time) {
	for id, set := range d.choiceSets {
		if !now.Before(set.expires) {
			delete(d.choiceSets, id)
		}
	}
}

func (d *Discord) replyChoices(ctx context.Context, i discordInteraction, text string, choices []DiscordChoice) error {
	if len(choices) == 0 {
		return d.reply(ctx, i, text)
	}
	if d.choiceSets == nil {
		d.choiceSets = make(map[string]discordChoiceSet)
	}
	now := time.Now()
	d.expireChoices(now)
	if len(d.choiceSets) >= 16 {
		return d.reply(ctx, i, "Слишком много незавершённых действий. Повторите позже.")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(random[:])
	set := discordChoiceSet{expires: now.Add(2 * time.Minute), actions: make(map[string]func() string)}
	var components []map[string]any
	for index, choice := range choices[:min(len(choices), 25)] {
		if index%5 == 0 {
			components = append(components, map[string]any{"type": 1, "components": []map[string]any{}})
		}
		row := components[len(components)-1]
		key := fmt.Sprintf("%s:%d", id, index)
		set.actions[key] = choice.Run
		row["components"] = append(row["components"].([]map[string]any), map[string]any{"type": 2, "style": 2, "label": truncate(choice.Label, 80), "custom_id": "control:" + key})
	}
	d.choiceSets[id] = set
	err := d.api.request(ctx, http.MethodPatch, "/webhooks/"+i.ApplicationID+"/"+i.Token+"/messages/@original", "reply", map[string]any{
		"content": truncate(text, 1900), "components": components, "allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
	if err != nil {
		delete(d.choiceSets, id)
	}
	return err
}
