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
		"lb-code":       {"📋 Код лота", "В карточке лота в Cardinal скопируйте его код, затем вставьте его сюда.", "lot-tools"},
		"lb-find":       {"🔎 Поиск кампаний", "Введите название игры или кампании либо вставьте ссылку Twitch Drops. Бот проверит Twitch и сохранённую историю, затем при необходимости проверит аккаунты лота.", "lb:" + ref + ":games:0"},
		"lb-inventory":  {"🎒 Поиск в инвентаре", "Введите название игры или кампании либо вставьте ссылку Twitch Drops. Бот проверит аккаунты из TXT этого лота.", "lb:" + ref + ":tools"},
		"lot-inventory": {"🎒 Поиск в инвентаре", "На первой строке отправьте название игры, кампании или ссылку Twitch Drops. На второй укажите логин Twitch аккаунта с её наградами.", "lot-tools"},
		"lot-catalog":   {"🏷 Кампания в каталоге лотов", "Отправьте название игры первой строкой. Второй строкой укажите ID кампании и название через |. Затем отправьте каждый дроп на отдельной строке.\n\nПример\nWar Thunder\n123 | WTCS Finals\n456 | Supply Crate\n\nЕсли известен точный срок для всей кампании, добавьте строку в формате\nСрок | 2026-11-23T18:00:00+03:00 | HTTPS-ссылка на правило.", "lot-tools"},
		"lot-discover":  {"🔎 Кампании Twitch", "Введите название игры или кампании либо вставьте ссылку Twitch Drops. Найденные кампании появятся в каталоге вместе с наградами.", "lot-catalog-page:0"},
		"lot-bundle":    {"🏷 Состав лота", "Первая строка — код лота из Cardinal. Дальше по одной кампании на строку. Для полной кампании укажите её ID. Для части кампании добавьте после ID через пробел только нужные ID наград.\n\nПример\nкод лота\n123\n789 456 457", "lot-tools"},
		"lot-link":      {"Привязка лота", "Скопируй код из карточки лота в боте Cardinal и отправь сюда.", "lot-preview"},
		"find":          {"🔎 Поиск игры", "Отправьте одним сообщением: целевой размер пула, затем часть названия игры. После поиска бот предложит выбрать точную категорию.\n\nПример: 100 Hitman", "games"},
		"add":           {"🎮 Добавление игры", "Отправьте одним сообщением: целевой размер пула, затем точное название категории Twitch.\n\nПример: 100 HITMAN World of Assassination", "games"},
		"simulate":      {"🧪 Симулятор расписания", "Отправьте один сценарий. Разделите части знаком |.\n\nПервая часть: число слотов и общий лимит аккаунтов.\nКаждая следующая: аккаунты, минуты на группу и минуты до дедлайна.\n\nПример: 1 20 | 30 30 90 | 20 45 120", "settings"},
		"digest":        {"🔔 Время суточной сводки", "Отправьте время в формате ЧЧ:ММ по часовому поясу сервера. Чтобы выключить сводку, отправьте off.\n\nПример: 21:00", "notifications"},
		"limits":        {"⚙️ Лимиты", "Отправьте два числа в одном сообщении, через пробел.\n\n1. Сколько игр может работать одновременно.\n2. Общий лимит работающих аккаунтов.\n\nПример: 2 1000", "settings"},
		"timing":        {"🗓 Расписание", "Отправьте три числа в одном сообщении, через пробел.\n\n1. Резерв на всю кампанию — минуты (1–1440).\n2. Скорость начисления — проценты (1–100).\n3. Интервал поиска кампаний — секунды (15–86400).\n\nПример: 10 75 120", "settings"},
		"assign":        {"👥 Закрепление аккаунта", "Отправьте логин аккаунта и точное название игры через пробел.\n\nПример: my_login HITMAN World of Assassination", "accounts"},
		"policy":        {"⚙️ Правила игры", "Отправьте режим и запас в минутах через пробел.\n\nРежим: default — общий, speed — скорость, resources — экономия.\nЗапас: 0 — общий, 1–1440 — свой.\n\nПример: resources 20", "game:" + ref},
		"upload":        {"📎 Загрузка в пул игры", "Прикрепите .txt как документ в течение пяти минут. Аккаунты будут добавлены в пул выбранной игры.", "game:" + ref},
		"free":          {"👥 Возврат в свободный пул", "Отправьте только логин Twitch.\n\nПример: my_login", "accounts"},
		"partial":       {"📤 Снять часть группы", "Отправьте число аккаунтов, которые нужно забрать. Покажем выборку по прогнозу времени до завершения.", "game-pool:" + ref},
		"target":        {"👥 Целевой размер пула", "Отправьте одно положительное число.\n\nПример: 100", "game:" + ref},
		"exclude":       {"🎁 Исключение награды", "Отправьте полное название награды одним сообщением. Регистр не важен.\n\nПример: Royal Purple Banana", "game:" + ref},
		"include":       {"🎁 Возврат награды в фарм", "Отправьте полное название исключённой награды одним сообщением.\n\nПример: Royal Purple Banana", "game:" + ref},
	}
	prompt, ok := prompts[kind]
	if !ok {
		return bot.SettingsPanel{Text: "Действие недоступно.", Buttons: [][]bot.SettingsButton{{{Label: "В меню", Action: "menu"}}}}
	}
	return bot.SettingsPanel{Title: prompt.title, Text: prompt.text, Input: input, Buttons: [][]bot.SettingsButton{{{Label: "Отмена", Action: prompt.back}}}}
}

func (s *scheduler) inputPrompt(input string) bot.SettingsPanel {
	if input == "upload:lot-credentials" {
		return bot.SettingsPanel{Title: "📎 Аккаунты для лотов", Input: input,
			Text:    "Прикрепите полный TXT как документ. По этим данным оркестратор сможет проверить лот, даже если в Cardinal только логин и пароль. Аккаунты сохранятся в разделе лотов.",
			Buttons: [][]bot.SettingsButton{{{Label: "⬅️ Дополнительно", Action: "lot-tools"}}}}
	}
	panel := telegramPrompt(input)
	kind, ref, _ := strings.Cut(input, ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.state.Settings
	switch kind {
	case "lb-find":
		if draft := s.lotBundleDraft; draft != nil && draft.Token == ref && draft.Game != "" {
			panel.Buttons[0][0].Action = "lb:" + ref + ":campaigns:0"
		}
	case "limits":
		panel.Text += fmt.Sprintf("\n\nСейчас: %d игр, %d аккаунтов.", cfg.MaxGames, cfg.MaxAccounts)
	case "timing":
		panel.Text += fmt.Sprintf("\n\nСейчас: запас %d мин, скорость %d%%, поиск каждые %d сек.", cfg.ReserveMinutes, cfg.ExpectedRatePercent, cfg.DiscoverySeconds)
	case "digest":
		panel.Text += "\n\n" + s.setDigest("")
	case "target", "policy", "upload", "exclude", "include", "partial":
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
	if ref == "lot-credentials" {
		return s.uploadLotCredentials(data)
	}
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
	if kind == "lb-code" {
		lot := strings.ToLower(strings.TrimSpace(text))
		if !validLotRef(lot) {
			return retry("Вставьте код из карточки Cardinal.")
		}
		return s.telegramLotBundle("lb-open", lot)
	}
	if kind == "lb-find" || kind == "lb-inventory" || kind == "lot-discover" || kind == "lot-inventory" {
		query, login := text, ""
		if kind == "lot-inventory" {
			var ok bool
			query, login, ok = strings.Cut(strings.TrimSpace(text), "\n")
			login = strings.TrimSpace(login)
			if !ok || !validLotLogin(login) {
				return retry("Нужны две строки. На второй укажите только логин Twitch.")
			}
		}
		query, valid := normalizeLotCampaignQuery(query)
		if !valid {
			return retry("Укажите название игры, кампании или ссылку Twitch Drops с ID кампании.")
		}
		var count int
		var err error
		fromLot := kind == "lb-inventory"
		if !fromLot {
			count, err = s.discoverLotCampaigns(query, login)
			fromLot = kind == "lb-find" && count == 0 && !s.hasLotCatalogMatch(query, ref)
			if kind == "lb-find" && !fromLot && count == 0 {
				err = nil // An existing catalog result remains available during a Twitch outage.
			}
		}
		if fromLot {
			catalogErr := err
			count, err = s.discoverLotSampleCampaigns(query, ref)
			if kind == "lb-find" && err != nil {
				status := "В каталоге Twitch и сохранённой истории совпадений нет."
				if catalogErr != nil {
					status = "Поиск в Twitch не подтвердил кампанию, в сохранённой истории совпадений нет."
				}
				return retry(status + "\nНе удалось проверить инвентарь аккаунтов лота. " + err.Error() + ".")
			}
		}
		if err != nil {
			return retry(err.Error())
		}
		var panel bot.SettingsPanel
		if kind == "lb-find" || kind == "lb-inventory" {
			panel = s.telegramLotSearchResult(ref, query)
		} else {
			catalogPage := "0"
			gameRef := lotCatalogGameRef(query)
			s.mu.Lock()
			_, campaigns := s.lotCatalogSelection(gameRef)
			s.mu.Unlock()
			if len(campaigns) > 0 {
				catalogPage = gameRef
			}
			panel = s.telegramLotCatalog(catalogPage)
		}
		if count > 0 {
			panel.Text = fmt.Sprintf("Кампаний добавлено в каталог · %d\n\n%s", count, panel.Text)
		} else if kind == "lb-find" || kind == "lb-inventory" {
			return panel
		} else if login != "" {
			panel.Text = "Инвентарь не добавил новых совпадений. Проверьте сохранённые кампании или попробуйте другой аккаунт из лота.\n\n" + panel.Text
		} else {
			panel.Text = "Новых кампаний нет. Завершившуюся кампанию можно поискать в инвентаре аккаунта из лота.\n\n" + panel.Text
		}
		return panel
	}
	if kind == "lot-catalog" || kind == "lot-bundle" {
		if err := s.saveManagedLotInput(kind, text); err != nil {
			return retry(err.Error())
		}
		panel := telegramLotTools()
		panel.Text = "Сохранено.\n\n" + panel.Text
		return panel
	}
	if kind == "lot-link" {
		ref := strings.ToLower(strings.TrimSpace(text))
		if !validLotRef(ref) {
			return retry("Нужен код лота из Cardinal.")
		}
		return s.telegramLotPreview("lot-preview-link", ref)
	}
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
	case "partial":
		count, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || count < 1 {
			return retry("Укажите одно положительное число.")
		}
		return s.previewPartialExport(ref, count, time.Now())
	case "find":
		return s.telegramFindGame(text)
	case "upload":
		return retry("Прикрепите файл как документ.")
	case "add":
		return s.telegramAddGame(text)
	case "limits", "timing", "digest", "simulate", "assign", "free":
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
