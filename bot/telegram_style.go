package bot

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

func telegramPanelTitle(action string) string {
	kind, _, _ := strings.Cut(action, ":")
	switch {
	case kind == "" || kind == "menu":
		return "🎮 TDM Light"
	case kind == "status":
		return "📡 Состояние оркестратора"
	case kind == "lagging" || kind == "game-lagging":
		return "🐢 Отстающие аккаунты"
	case kind == "stats" || kind == "game-stats":
		return "📊 Статистика фарма"
	case kind == "games" || kind == "find-choice":
		return "🎮 Игры и кампании"
	case kind == "accounts":
		return "👥 Пулы аккаунтов"
	case kind == "notifications" || kind == "notify":
		return "🔔 Уведомления"
	case kind == "help" || kind == "help-commands":
		return "📖 Справка"
	case kind == "logs":
		return "📁 Дневные логи"
	case kind == "quarantine" || kind == "game-quarantine":
		return "🛡 Карантин"
	case kind == "report":
		return "📋 История кампаний"
	case strings.Contains(kind, "preview"):
		return "🗓 Предпросмотр расписания"
	case strings.HasPrefix(kind, "export"):
		return "📄 Выгрузка аккаунтов"
	case strings.HasPrefix(kind, "clear-") || strings.HasPrefix(kind, "remove"):
		return "⚠️ Подтверждение операции"
	case strings.HasPrefix(kind, "regroup"):
		return "🔀 Перегруппировка"
	case kind == "prompt":
		return "✍️ Ввод данных"
	case kind == "game" || kind == "stop" || kind == "resume" || kind == "restart":
		return "🎮 Управление игрой"
	case kind == "settings" || kind == "miner-options" || kind == "miner-option" || kind == "speed" || kind == "resources":
		return "⚙️ Настройки"
	case kind == "trim" || kind == "trim-confirm":
		return "⚡ Нагрузка"
	default:
		return "🎮 Оркестратор TDM Light"
	}
}

func formatTelegramPanel(action, title, text string) string {
	var out strings.Builder
	if title == "" {
		title = telegramPanelTitle(action)
	}
	out.WriteString("<b>" + html.EscapeString(title) + "</b>")
	text = truncate(strings.TrimSpace(text), 3400)
	if text == "" {
		return out.String()
	}
	out.WriteString("\n\n")
	kind, _, _ := strings.Cut(action, ":")
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		escaped := html.EscapeString(line)
		switch {
		case (kind == "menu" || kind == "") && i == 0:
			out.WriteString("<i>" + escaped + "</i>")
		case (kind == "menu" || kind == "") && strings.HasPrefix(line, "🎮 Игры:"):
			games, accounts, _ := strings.Cut(line, "   ·   ")
			for j, item := range []string{games, accounts} {
				if j > 0 {
					out.WriteString("   ·   ")
				}
				label, count, _ := strings.Cut(item, ": ")
				out.WriteString(html.EscapeString(label) + " <b>" + html.EscapeString(count) + "</b>")
			}
		case telegramSectionHeading(line) || strings.HasPrefix(line, "🎮 ") || strings.HasPrefix(line, "🏆 ") ||
			strings.HasPrefix(line, "⏳ ") || strings.HasPrefix(line, "🔴 ") || line == "Самые медленные:":
			out.WriteString("<b>" + escaped + "</b>")
		case kind == "status" && strings.HasPrefix(line, "• "):
			out.WriteString("<b>" + escaped + "</b>")
		case strings.HasPrefix(line, "Пример: "):
			out.WriteString("<b>Пример:</b> <code>" + html.EscapeString(strings.TrimPrefix(line, "Пример: ")) + "</code>")
		case strings.HasPrefix(line, "Сейчас: "):
			out.WriteString("<b>Сейчас:</b>" + strings.TrimPrefix(escaped, "Сейчас:"))
		case kind == "report" && strings.Contains(line, " · до "):
			name, details, _ := strings.Cut(line, " · ")
			out.WriteString("<b>" + html.EscapeString(name) + "</b>\n" + html.EscapeString(details))
		case i == 0 && kind == "game":
			out.WriteString("<b>" + escaped + "</b>")
		case strings.HasPrefix(line, "Сегодня:") || strings.HasPrefix(line, "За 7 календарных дней:") || strings.HasPrefix(line, "За всё время:"):
			label, rest, _ := strings.Cut(line, ":")
			out.WriteString("<b>" + html.EscapeString(label) + ":</b>" + html.EscapeString(rest))
		case strings.HasPrefix(line, "ℹ️ ") || strings.HasPrefix(line, "🕒 "):
			out.WriteString("<i>" + escaped + "</i>")
		case strings.Contains(line, " · пул ") && kind == "status":
			out.WriteString("<b>" + escaped + "</b>")
		default:
			label, value, ok := strings.Cut(line, ": ")
			if ok && telegramMetricLabel(label) {
				out.WriteString("<b>" + html.EscapeString(label) + ":</b> " + html.EscapeString(value))
			} else {
				out.WriteString(escaped)
			}
		}
	}
	return out.String()
}

func telegramSectionHeading(line string) bool {
	switch line {
	case "📊 Загрузка", "⚙️ Настройки фарма", "🟣 Связь с Twitch", "📡 Фарм сейчас",
		"📊 Нагрузка плана", "📡 Текущее состояние", "⚙️ Текущие настройки", "🎛 Параметры майнеров",
		"📥 Свободный пул", "🔔 Доставка", "🕘 Суточная сводка", "📅 Дата архива", "📦 Содержимое ZIP",
		"📅 Сегодня", "🗓 Последние 7 дней", "🏁 За всё время":
		return true
	}
	return false
}

func telegramMetricLabel(label string) bool {
	switch label {
	case "Пик аккаунтов", "Аккаунты", "Группы", "Старт", "Финиш", "Резерв кампании",
		"До дедлайна сверх резерва", "Режим", "Лимит", "Стартовая скорость", "Опрос Twitch",
		"Игры в работе", "Аккаунты в работе", "Свободный пул", "Карантин", "Последний успешный опрос",
		"Последний успешный опрос Twitch", "Активная группа", "В очереди", "Отстают", "Проверено",
		"Среднее отставание", "Подтверждённых эфиров кампании", "Игра", "Игр в списке", "Следующая группа", "Первая группа",
		"Аккаунты в пуле", "Завершили", "Готово к получению", "Получено", "Статус получения неизвестен",
		"Выбрано кампаний", "Условие", "Подходят":
		return true
	}
	return false
}

func telegramButton(action, label string) string {
	if label == "Обновить" {
		return "🔄 " + label
	}
	kind, value, _ := strings.Cut(action, ":")
	if label == "Отмена" {
		return "↩️ " + label
	}
	if label == "Предыдущие" {
		return "⬅️ " + label
	}
	if label == "Следующие" {
		return "➡️ " + label
	}
	if strings.HasPrefix(label, "✓ ") {
		return "🟢 " + strings.TrimPrefix(label, "✓ ")
	}
	if strings.HasPrefix(label, "○ ") {
		return "⚪ " + strings.TrimPrefix(label, "○ ")
	}
	if first, _ := utf8.DecodeRuneInString(label); unicode.Is(unicode.So, first) {
		return label
	}
	if kind == "help-commands" {
		return "⌨️ " + label
	}
	if kind == "help" {
		icon := map[string]string{"games": "🎮", "accounts": "👥", "settings": "⚙️", "load": "⚡", "notifications": "🔔", "stats": "📊"}[value]
		if icon == "" {
			icon = "📖"
		}
		return icon + " " + label
	}
	if kind == "prompt" {
		kind, _, _ = strings.Cut(value, ":")
	}
	var icon string
	switch {
	case confirmationAction(action):
		if strings.HasPrefix(kind, "clear-") || strings.HasPrefix(kind, "remove-") || strings.HasPrefix(kind, "export-clear-") || strings.HasPrefix(kind, "export-remove-") || kind == "trim-confirm" {
			icon = "⚠️"
		} else {
			icon = "✅"
		}
	case strings.Contains(kind, "preview"):
		icon = "🔎"
	case strings.HasPrefix(kind, "regroup"):
		icon = "🔀"
	case strings.HasPrefix(kind, "export"):
		icon = "📄"
	case strings.HasPrefix(kind, "clear-") || kind == "remove":
		icon = "🗑️"
	default:
		icon = map[string]string{
			"status": "📡", "stats": "📊", "game-stats": "📊", "games": "🎮", "game": "🎮", "find-choice": "🎮",
			"accounts": "👥", "game-pool": "👥", "game-options": "⚙️", "assign": "📌", "free": "📤", "target": "🎯", "upload": "📎",
			"settings": "⚙️", "miner-options": "⚙️", "policy": "⚙️", "notifications": "🔔", "digest": "🕘",
			"help": "📖", "menu": "🏠", "resume": "▶️", "stop": "⏸️", "restart": "🔄", "report": "📋",
			"lagging": "🐢", "game-lagging": "🐢", "quarantine": "🛡️", "game-quarantine": "🛡️", "speed": "🔁", "resources": "🔁", "limits": "🎚️",
			"timing": "🗓️", "simulate": "🧪", "trim": "⚡", "find": "🔎", "add": "➕", "exclude": "🚫", "include": "🎁",
		}[kind]
		if kind == "notify" {
			if strings.HasSuffix(value, ":on") {
				icon = "🔔"
			} else {
				icon = "🔕"
			}
		}
		if icon == "" {
			icon = "🔹"
		}
	}
	return icon + " " + label
}
