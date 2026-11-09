package orchestrator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func (s *scheduler) discordAction(ctx context.Context, action string, options map[string]string, data []byte) (string, []bot.DiscordChoice) {
	switch action {
	case "find_game":
		query := strings.TrimSpace(options["query"])
		target, err := strconv.Atoi(strings.TrimSpace(options["target"]))
		if len(query) < 2 || len(query) > 200 || err != nil || target < 1 {
			return "Укажите название для поиска и положительный целевой размер пула.", nil
		}
		names, err := s.searchCategories(ctx, query)
		if err != nil {
			return err.Error(), nil
		}
		if len(names) == 0 {
			return "Категории не найдены. Уточните название.", nil
		}
		choices := make([]bot.DiscordChoice, 0, len(names))
		for _, name := range names {
			choices = append(choices, bot.DiscordChoice{Label: name, Run: func() string { return s.operate("add", strconv.Itoa(target)+" "+name) }})
		}
		return fmt.Sprintf("Выберите категорию Twitch. Целевой пул: %d.", target), choices
	case "accounts":
		game := strings.ToLower(strings.TrimSpace(options["game"]))
		if err := s.applyUpload(data, game, false); err != nil {
			return "Файл не принят: " + err.Error(), nil
		}
		groups, err := parser.Parse(strings.NewReader(string(data)))
		if err != nil {
			return "Файл не принят: " + err.Error(), nil
		}
		count := 0
		for _, group := range groups {
			count += len(group)
		}
		pool := "Свободный пул"
		if game != "" {
			pool = game
		}
		return fmt.Sprintf("Пул: %s. Аккаунтов в файле: %d. Подтвердите загрузку или оставьте запрос без ответа.", pool, count),
			[]bot.DiscordChoice{{Label: "Подтвердить загрузку", Run: func() string {
				if err := s.uploadToPool(data, game); err != nil {
					return "Файл не сохранён: " + err.Error()
				}
				return fmt.Sprintf("Сохранено аккаунтов: %d. Пул: %s.", count, pool)
			}}}
	case "clear_pool", "remove_game":
		name := options["pool"]
		if action == "remove_game" {
			name = options["game"]
		}
		return s.discordPoolAction(action, strings.ToLower(strings.TrimSpace(name)))
	}
	return "Действие недоступно.", nil
}

func (s *scheduler) poolFingerprint(key string) (string, int) {
	hash := sha256.New()
	count := 0
	for _, a := range s.state.Accounts {
		if a.Game != key {
			continue
		}
		c := a.Credentials
		fmt.Fprintf(hash, "%q %q %q %q %q %q\n", c.Email, c.Login, c.Password, c.OAuth, c.UserID, c.DeviceID)
		count++
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), count
}

func (s *scheduler) discordPoolAction(action, key string) (string, []bot.DiscordChoice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	free := action == "clear_pool" && key == "free"
	if free {
		key = ""
	}
	label := "Свободный пул"
	var g *gameState
	if !free {
		g = s.state.Games[key]
		if g == nil || g.Removing {
			return "Игра не найдена или уже удаляется.", nil
		}
		if action == "clear_pool" && (g.Regroup || s.active[key] != nil) {
			return "Сначала остановите игру и дождитесь окончания операции с её майнером.", nil
		}
		label = g.Name
	}
	fingerprint, count := s.poolFingerprint(key)
	choice := func(disposition, title string) bot.DiscordChoice {
		return bot.DiscordChoice{Label: title, Run: func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			if current, _ := s.poolFingerprint(key); current != fingerprint || (!free && s.state.Games[key] != g) {
				return "Состав пула изменился. Запустите команду заново."
			}
			if action == "remove_game" {
				return s.requestGameRemoval(key, disposition, true)
			}
			scope := "free"
			if !free {
				scope = "game:" + key
			}
			return s.clearPool(scope, disposition, true)
		}}
	}
	message := fmt.Sprintf("Пул «%s»: %d аккаунтов. Выберите, что с ними сделать. Подтверждение действует 2 минуты.", label, count)
	if action == "remove_game" {
		message += " Игра и история кампаний будут удалены."
	}
	if free {
		return message, []bot.DiscordChoice{choice("delete", "Удалить записи аккаунтов")}
	}
	return message, []bot.DiscordChoice{choice("release", "Вернуть в свободный пул"), choice("delete", "Удалить записи аккаунтов")}
}
