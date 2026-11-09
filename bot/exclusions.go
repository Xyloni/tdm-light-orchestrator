package bot

import "strings"

func exclusionCommand(name string, exclude bool, get func() []string, set func(string, bool) error) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		if !exclude {
			return "Использование: /include полное название награды", false
		}
		if get == nil {
			return "Настройка исключений недоступна.", false
		}
		names := get()
		if len(names) == 0 {
			return "Список исключений пуст. Добавить: /exclude полное название награды", false
		}
		return "Исключённые награды (полное название без учёта регистра):\n• " + strings.Join(names, "\n• ") + "\nВернуть: /include название", false
	}
	if set == nil {
		return "Настройка исключений недоступна.", false
	}
	if err := set(name, exclude); err != nil {
		return "Не удалось изменить исключения: " + err.Error(), false
	}
	if exclude {
		return "Исключено из фарма: " + name + ". Перезапускаю обработку групп.", true
	}
	return "Возвращено в фарм: " + name + ". Перезапускаю обработку групп.", true
}
