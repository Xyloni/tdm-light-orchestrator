package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
)

func editableSettings(cfg *config.Config) map[string]string {
	return map[string]string{
		"STOP_WHEN_IDLE":    strconv.FormatBool(cfg.StopWhenIdle),
		"IDLE_CHECK_ROUNDS": strconv.Itoa(cfg.IdleCheckRounds),
		"DIAGNOSTICS":       strconv.FormatBool(cfg.Diagnostics),
		"SINGLE_LOG":        strconv.FormatBool(cfg.SingleLog),
	}
}

func (m *Manager) settingsAt(filePath, action string) (bot.SettingsPanel, bool) {
	m.settingsMu.Lock()
	defer m.settingsMu.Unlock()
	saved, err := config.Load(filepath.Join(filepath.Dir(filePath), "config.json"), filePath)
	if err != nil {
		return bot.SettingsPanel{Text: "Не удалось прочитать настройки."}, false
	}
	values := editableSettings(saved)
	fields := strings.Fields(action)
	message := ""
	reload := false
	switch {
	case len(fields) == 0 || (len(fields) == 1 && fields[0] == "show"):
	case len(fields) == 2 && fields[0] == "apply":
		switch fields[1] {
		case "now":
			m.deferredReload.Store(false)
			message = "Применяю настройки с перезапуском обработки групп."
			reload = true
		case "after":
			m.deferredReload.Store(true)
			message = "Настройки применятся после завершения текущей группы."
			if len(m.groups) == 0 {
				message = "Групп нет. Применяю настройки сейчас."
				reload = true
			}
		default:
			message = "Используйте apply now или apply after."
		}
	case len(fields) == 2:
		key := strings.ToUpper(fields[0])
		if _, ok := values[key]; !ok {
			message = "Эта настройка недоступна через бота."
			break
		}
		if _, locked := os.LookupEnv(key); locked {
			message = "Настройка закреплена окружением контейнера. Удалите " + key + " из окружения для управления ботом."
			break
		}
		value := strings.ToLower(fields[1])
		if key == "IDLE_CHECK_ROUNDS" {
			n, parseErr := strconv.Atoi(value)
			if parseErr != nil || n < 1 || n > 100 {
				message = "Количество проверок должно быть от 1 до 100."
				break
			}
			value = strconv.Itoa(n)
		} else if value != "true" && value != "false" {
			message = "Допустимые значения: true или false."
			break
		}
		if err := updateEnvSetting(filePath, key, value); err != nil {
			message = "Не удалось сохранить настройку."
			break
		}
		values[key] = value
		message = "Сохранено. Выберите момент применения."
	default:
		message = "Использование: /settings KEY VALUE, /settings apply now или /settings apply after."
	}
	active := editableSettings(m.cfg)
	text := "Настройки (сохранённые значения)\n"
	labels := map[string]string{"STOP_WHEN_IDLE": "Автовыключение", "IDLE_CHECK_ROUNDS": "Пустых проверок", "DIAGNOSTICS": "Диагностика", "SINGLE_LOG": "Общий лог"}
	for _, key := range []string{"STOP_WHEN_IDLE", "IDLE_CHECK_ROUNDS", "DIAGNOSTICS", "SINGLE_LOG"} {
		text += labels[key] + ": " + values[key]
		if active[key] != values[key] {
			text += " (сейчас " + active[key] + ")"
		}
		text += "\n"
	}
	if m.deferredReload.Load() {
		text += "Применение запланировано после текущей группы.\n"
	}
	text += "Игра: /game · Награды: /exclude и /include\nПосле автовыключения бот тоже остановится."
	if message != "" {
		text = message + "\n\n" + text
	}
	button := func(key, label string) bot.SettingsButton {
		next := "true"
		if values[key] == "true" {
			next = "false"
		}
		return bot.SettingsButton{Label: label + " → " + next, Action: key + " " + next}
	}
	rounds, _ := strconv.Atoi(values["IDLE_CHECK_ROUNDS"])
	roundButtons := []bot.SettingsButton{}
	if rounds > 1 {
		roundButtons = append(roundButtons, bot.SettingsButton{Label: "Проверок −", Action: fmt.Sprintf("IDLE_CHECK_ROUNDS %d", rounds-1)})
	}
	if rounds < 100 {
		roundButtons = append(roundButtons, bot.SettingsButton{Label: "Проверок +", Action: fmt.Sprintf("IDLE_CHECK_ROUNDS %d", rounds+1)})
	}
	return bot.SettingsPanel{Text: text, Buttons: [][]bot.SettingsButton{
		{button("STOP_WHEN_IDLE", "Автовыключение")},
		{button("DIAGNOSTICS", "Диагностика"), button("SINGLE_LOG", "Общий лог")},
		roundButtons,
		{{Label: "Применить сейчас", Action: "apply now"}, {Label: "После группы", Action: "apply after"}, {Label: "Обновить", Action: "show"}},
	}}, reload
}
