package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func telegramPrompt(input string) bot.SettingsPanel {
	kind, ref, _ := strings.Cut(input, ":")
	prompts := map[string]struct{ title, text, back string }{
		"find":     {"🔎 Поиск игры", "Отправьте одним сообщением: целевой размер пула, затем часть названия игры. После поиска бот предложит выбрать точную категорию.\n\nПример: 100 Hitman", "games"},
		"add":      {"🎮 Добавление игры", "Отправьте одним сообщением: целевой размер пула, затем точное название категории Twitch.\n\nПример: 100 HITMAN World of Assassination", "games"},
		"simulate": {"🧪 Симулятор расписания", "Отправьте один сценарий. Разделите части знаком |.\n\nПервая часть: число слотов и общий лимит аккаунтов.\nКаждая следующая: аккаунты, минуты на группу и минуты до дедлайна.\n\nПример: 1 20 | 30 30 90 | 20 45 120", "settings"},
		"digest":   {"🔔 Время суточной сводки", "Отправьте время в формате ЧЧ:ММ по часовому поясу сервера. Чтобы выключить сводку, отправьте off.\n\nПример: 21:00", "notifications"},
		"limits":   {"⚙️ Лимиты", "Отправьте два числа в одном сообщении, через пробел.\n\n1. Сколько игр может работать одновременно.\n2. Общий лимит работающих аккаунтов.\n\nПример: 2 1000", "settings"},
		"timing":   {"🗓 Расписание", "Отправьте три числа в одном сообщении, через пробел.\n\n1. Резерв на всю кампанию — минуты (1–1440).\n2. Скорость начисления — проценты (1–100).\n3. Интервал поиска кампаний — секунды (15–86400).\n\nПример: 10 75 120", "settings"},
		"assign":   {"👥 Закрепление аккаунта", "Отправьте логин аккаунта и точное название игры через пробел.\n\nПример: my_login HITMAN World of Assassination", "accounts"},
		"policy":   {"⚙️ Правила игры", "Отправьте режим и запас в минутах через пробел.\n\nРежим: default — общий, speed — скорость, resources — экономия.\nЗапас: 0 — общий, 1–1440 — свой.\n\nПример: resources 20", "game:" + ref},
		"upload":   {"📎 Загрузка в пул игры", "Прикрепите .txt как документ в течение пяти минут. Аккаунты будут добавлены в пул выбранной игры.", "game:" + ref},
		"free":     {"👥 Возврат в свободный пул", "Отправьте только логин Twitch.\n\nПример: my_login", "accounts"},
		"target":   {"👥 Целевой размер пула", "Отправьте одно положительное число.\n\nПример: 100", "game:" + ref},
		"exclude":  {"🎁 Исключение награды", "Отправьте полное название награды одним сообщением. Регистр не важен.\n\nПример: Royal Purple Banana", "game:" + ref},
		"include":  {"🎁 Возврат награды в фарм", "Отправьте полное название исключённой награды одним сообщением.\n\nПример: Royal Purple Banana", "game:" + ref},
	}
	prompt, ok := prompts[kind]
	if !ok {
		return bot.SettingsPanel{Text: "Действие недоступно.", Buttons: [][]bot.SettingsButton{{{Label: "В меню", Action: "menu"}}}}
	}
	return bot.SettingsPanel{Title: prompt.title, Text: prompt.text, Input: input, Buttons: [][]bot.SettingsButton{{{Label: "Отмена", Action: prompt.back}}}}
}

func (s *scheduler) inputPrompt(input string) bot.SettingsPanel {
	panel := telegramPrompt(input)
	kind, ref, _ := strings.Cut(input, ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.state.Settings
	switch kind {
	case "limits":
		panel.Text += fmt.Sprintf("\n\nСейчас: %d игр, %d аккаунтов.", cfg.MaxGames, cfg.MaxAccounts)
	case "timing":
		panel.Text += fmt.Sprintf("\n\nСейчас: запас %d мин, скорость %d%%, поиск каждые %d сек.", cfg.ReserveMinutes, cfg.ExpectedRatePercent, cfg.DiscoverySeconds)
	case "digest":
		panel.Text += "\n\n" + s.setDigest("")
	case "target", "policy", "upload", "exclude", "include":
		for key, g := range s.state.Games {
			if gameRef(key) != ref {
				continue
			}
			panel.Text = "Игра: " + g.Name + "\n\n" + panel.Text
			if kind == "target" {
				panel.Text += fmt.Sprintf("\n\nСейчас: %d аккаунтов.", g.Target)
			}
			if kind == "policy" {
				current := s.gameSettings(g)
				panel.Text += fmt.Sprintf("\n\nСейчас: %s, запас %d мин.", current.modeName(), current.ReserveMinutes)
			}
			break
		}
	}
	return panel
}

func validTelegramNumbers(fields []string, minimum, maximum []int) bool {
	if len(fields) != len(minimum) {
		return false
	}
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < minimum[i] || (maximum != nil && n > maximum[i]) {
			return false
		}
	}
	return true
}

func (s *scheduler) telegramUpload(ref string, data []byte) error {
	s.mu.Lock()
	var key string
	for candidate := range s.state.Games {
		if gameRef(candidate) == ref {
			key = candidate
			break
		}
	}
	s.mu.Unlock()
	if key == "" {
		return fmt.Errorf("игра не найдена")
	}
	return s.uploadToPool(data, key)
}

func (s *scheduler) telegramInput(input, text string) bot.SettingsPanel {
	kind, ref, _ := strings.Cut(input, ":")
	retry := func(message string) bot.SettingsPanel {
		panel := s.inputPrompt(input)
		panel.Text = "⚠️ " + message + "\n\n" + panel.Text
		return panel
	}
	fields := strings.Fields(text)
	switch kind {
	case "limits":
		if !validTelegramNumbers(fields, []int{1, 1}, nil) {
			return retry("Нужны два положительных числа.")
		}
	case "timing":
		if !validTelegramNumbers(fields, []int{1, 1, 15}, []int{1440, 100, 86400}) {
			return retry("Проверьте порядок чисел и допустимые диапазоны.")
		}
	case "target":
		if !validTelegramNumbers(fields, []int{1}, nil) {
			return retry("Нужно одно положительное число.")
		}
		text = fields[0]
	case "policy":
		if len(fields) != 2 || (fields[0] != "default" && fields[0] != "speed" && fields[0] != "resources") || !validTelegramNumbers(fields[1:], []int{0}, []int{1440}) {
			return retry("Выберите режим и укажите запас от 0 до 1440 минут.")
		}
	case "digest":
		parsed, err := time.Parse("15:04", text)
		if text != "off" && (err != nil || parsed.Format("15:04") != text) {
			return retry("Введите время в формате ЧЧ:ММ или off.")
		}
	}
	var message string
	switch kind {
	case "find":
		return s.telegramFindGame(text)
	case "upload":
		return retry("Прикрепите файл как документ.")
	case "add", "limits", "timing", "digest", "simulate", "assign", "free":
		message = s.operate(kind, text)
	case "target", "exclude", "include", "policy":
		s.mu.Lock()
		var key string
		for candidate := range s.state.Games {
			if gameRef(candidate) == ref {
				key = candidate
				break
			}
		}
		if key == "" {
			message = "Игра не найдена."
		} else if kind == "policy" {
			mode := fields[0]
			if mode == "default" {
				mode = ""
			}
			message = s.changeGamePolicy(key, mode, fields[1])
		} else if kind == "target" {
			s.mu.Unlock()
			return bot.SettingsPanel{Text: s.operate("target", text+" "+key), Buttons: [][]bot.SettingsButton{{{Label: "К игре", Action: "game:" + ref}, {Label: "В меню", Action: "menu"}}}}
		} else {
			message = s.changeExclusion(key, text, kind == "exclude")
		}
		s.mu.Unlock()
	default:
		message = "Действие недоступно."
	}
	return bot.SettingsPanel{Text: message, Buttons: [][]bot.SettingsButton{{{Label: "В меню", Action: "menu"}}}}
}
