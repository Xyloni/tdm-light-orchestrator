package orchestrator

import (
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

type telegramHelpTopic struct {
	title, description, destination, action, commands string
}

var telegramHelpTopics = map[string]telegramHelpTopic{
	"games": {
		"🎮 Игры и кампании",
		"Откройте «Игры» и выберите нужную игру. В её карточке можно поставить майнер на паузу, возобновить или перезапустить его, посмотреть историю кампаний и исключить награду.\n\nЧтобы добавить игру, нажмите «Найти игру в Twitch» и выберите категорию из результатов.",
		"Открыть игры", "games",
		"/pause_game — приостановить игру.\nПример: /pause_game HITMAN World of Assassination\n\n/resume_game — возобновить игру.\n/restart_game — перезапустить её майнер.\n\nПосле команды укажите полное название игры.",
	},
	"accounts": {
		"👥 Аккаунты и пулы",
		"Для свободного пула отправьте .txt как документ в личку боту. Для загрузки в конкретную игру откройте её карточку и нажмите «Загрузить аккаунты».\n\nВ разделе «Аккаунты» можно увидеть карантин, выгрузить свободный пул и изменить привязку аккаунта. При очистке пула бот попросит подтверждение.",
		"Открыть аккаунты", "accounts",
		"/quarantine — список аккаунтов в карантине.\n\nПример: /assign_account my_login HITMAN World of Assassination\nПример: /release_account my_login\n\nФормат строки в .txt: email:login:password:oauth_token:user_ID:device_ID",
	},
	"settings": {
		"⚙️ Настройки фарма",
		"В «Настройках» выберите режим, измените общие лимиты или расписание. Текущие значения и порядок ввода бот покажет перед изменением.\n\nОсобый режим или запас для одной игры задаётся в её карточке, в разделе «Правила игры».",
		"Открыть настройки", "settings",
		"Пример: /set_limits 2 1000\nДве активные игры и общий лимит 1000 аккаунтов.\n\nПример: /set_timing 10 75 120\nЗапас 10 минут, скорость 75%, поиск каждые 120 секунд.\n\nПример: /set_mode resources\nЭкономия ресурсов. Для скорости используйте speed.",
	},
	"load": {
		"⚡ Группы и нагрузка",
		"В «Настройках» откройте предпросмотр, чтобы сравнить режимы и увидеть прогноз групп и дедлайнов. Перегруппировка действующих майнеров применяется после подтверждения.\n\nЕсли превышен общий лимит, кнопка снижения нагрузки сначала покажет, сколько отстающих аккаунтов можно остановить.",
		"Открыть предпросмотр", "preview-all",
		"Пример: /preview_schedule all | resources\nПоказать прогноз в режиме экономии.\n\n/reduce_load — рассчитать снижение нагрузки.\n/reduce_load confirm — подтвердить остановку после расчёта.",
	},
	"notifications": {
		"🔔 Уведомления",
		"Откройте «Уведомления» и нажмите на нужное событие, чтобы включить или отключить его. Общий выключатель сохраняет выбранные настройки.\n\nТам же можно задать время суточной сводки. Она показывает итоги предыдущего календарного дня.",
		"Открыть уведомления", "notifications",
		"/notifications — посмотреть настройки доставки.\n\nПример: /daily_digest 21:00\nСводка в 21:00 по времени сервера. Для отключения отправьте /daily_digest off.",
	},
	"stats": {
		"📊 Статус и статистика",
		"«Статус» показывает, что фармится сейчас и чего ждут игры. «Статистика» показывает накопленные минуты, награды, кампании и группы за сегодня, неделю и всё время. Статистика отдельной игры доступна в её карточке.",
		"Открыть статистику", "stats",
		"/status — текущая очередь и состояние фарма.\n/stats — общая статистика.\n\nПример: /stats HITMAN World of Assassination",
	},
}

func telegramHelp(topic string, commands bool) bot.SettingsPanel {
	key := strings.ToLower(strings.TrimSpace(topic))
	section, ok := telegramHelpTopics[key]
	if !ok {
		text := "Выберите тему. В каждом разделе есть кнопка к нужному экрану и примеры команд.\n\nЕсли только начинаете: добавьте игру, загрузите аккаунты и откройте статус."
		if key != "" {
			text = "Раздел не найден.\n\n" + text
		}
		return bot.SettingsPanel{Title: "📖 Справка", Text: text, Buttons: [][]bot.SettingsButton{
			{{Label: "Игры", Action: "help:games"}, {Label: "Аккаунты", Action: "help:accounts"}},
			{{Label: "Настройки", Action: "help:settings"}, {Label: "Нагрузка", Action: "help:load"}},
			{{Label: "Уведомления", Action: "help:notifications"}, {Label: "Статистика", Action: "help:stats"}},
			{{Label: "В меню", Action: "menu"}},
		}}
	}
	if commands {
		return bot.SettingsPanel{Title: section.title + " · команды", Text: section.commands, Buttons: [][]bot.SettingsButton{
			{{Label: "К разделу", Action: "help:" + key}, {Label: "Все темы", Action: "help"}},
		}}
	}
	return bot.SettingsPanel{Title: section.title, Text: section.description, Buttons: [][]bot.SettingsButton{
		{{Label: section.destination, Action: section.action}},
		{{Label: "Команды и примеры", Action: "help-commands:" + key}, {Label: "Все темы", Action: "help"}},
	}}
}
