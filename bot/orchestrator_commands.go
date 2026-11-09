package bot

import "strings"

type orchestratorCommand struct {
	name, action, description, option, hint string
	required                                bool
}

var orchestratorCommands = []orchestratorCommand{
	{"quarantine", "quarantine", "Аккаунты в карантине", "game", "Оставьте пустым для всех игр или укажите игру", false},
	{"campaign_report", "campaign_report", "История кампаний игры", "game", "Полное название игры", true},
	{"game_policy", "game_policy", "Правила группировки и запас отдельной игры", "details", "Игра | режим (default, speed, resources) и запас (0 = общий)", true},
	{"help", "help", "Справка по командам", "topic", "games, accounts, settings или load", false},
	{"preview_schedule", "preview", "Предпросмотр расписания по игре или всем играм", "details", "all или название игры, затем необязательно | speed или | resources", true},
	{"simulate_schedule", "simulate", "Тестовый расчёт расписания без запуска", "scenario", "слоты лимит | аккаунты минуты_группы минуты_до_дедлайна", false},
	{"daily_digest", "digest", "Настроить суточную сводку", "time", "Время сервера HH:MM, off для отключения или пусто для статуса", false},
	{"lagging", "lagging", "Отстающие аккаунты и скорость начисления", "game", "Оставьте пустым для всех игр или укажите игру", false},
	{"stats", "stats", "Минуты, награды и кампании за разные периоды", "game", "Оставьте пустым для общей статистики или укажите игру", false},
	{"status", "status", "Кампании, группы и состояние Twitch", "", "", false},
	{"add_game", "add", "Добавить игру", "details", "Целевой размер и название, например 100 HITMAN", true},
	{"set_pool_target", "target", "Изменить целевой размер пула", "details", "Размер и название игры, например 100 HITMAN", true},
	{"pause_game", "stop", "Поставить игру на паузу", "game", "Полное название игры", true},
	{"resume_game", "resume", "Возобновить игру", "game", "Полное название игры", true},
	{"restart_game", "restart", "Перезапустить майнер игры", "game", "Полное название игры", true},
	{"remove_game", "remove", "Удалить игру с подтверждением", "details", "Название игры, затем confirm release или confirm delete и название", true},
	{"game_errors", "errors", "Пропуски по игре", "game", "Полное название игры", true},
	{"excluded_rewards", "exclusions", "Показать исключённые награды", "game", "Полное название игры", true},
	{"exclude_reward", "exclude", "Исключить награду", "details", "Название игры | Полное название награды", true},
	{"include_reward", "include", "Вернуть награду в фарм", "details", "Название игры | Полное название награды", true},
	{"assign_account", "assign", "Закрепить аккаунт за игрой", "details", "Логин и название игры", true},
	{"release_account", "free", "Вернуть аккаунт в свободный пул", "login", "Логин Twitch", true},
	{"set_mode", "mode", "Режим группировки", "mode", "speed или resources", true},
	{"set_limits", "limits", "Лимиты работы", "details", "Количество игр и аккаунтов, например 2 1000", true},
	{"set_timing", "timing", "Расписание", "details", "Запас в минутах, скорость в процентах и интервал в секундах", true},
	{"miner_settings", "miner", "Настройки дочерних майнеров", "details", "Например diagnostics on, single_log off или pubsub on", true},
	{"regroup", "regroup", "Перерасчёт групп с подтверждением", "details", "all, название игры или тот же выбор после слова confirm", true},
	{"reduce_load", "trim", "Снизить нагрузку с подтверждением", "confirmation", "Оставьте пустым для расчёта или введите confirm", false},
	{"notifications", "notifications", "Настройки уведомлений", "details", "Оставьте пустым для списка или введите тип и on/off", false},
}

func findOrchestratorCommand(name string) (orchestratorCommand, bool) {
	for _, cmd := range orchestratorCommands {
		if cmd.name == name {
			return cmd, true
		}
	}
	return orchestratorCommand{}, false
}

func (c orchestratorCommand) input(value string) string {
	return strings.TrimSpace(c.action + " " + strings.TrimSpace(value))
}
