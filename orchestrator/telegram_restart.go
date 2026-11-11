package orchestrator

import (
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func (s *scheduler) telegramRestart(action string, now time.Time) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "🔄 Перезапуск оркестратора"}
	button := func(label, target string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: target}})
	}
	if action == "restart-all-cancel" {
		s.restartStep = 0
		panel.Text = "Перезапуск отменён."
		button("К настройкам", "settings")
		return panel
	}
	switch {
	case action == "restart-all":
		s.restartStep = 1
		s.restartUntil = now.Add(2 * time.Minute)
		panel.Text = "Все майнеры остановятся. Оркестратор заново загрузит сохранённое состояние."
		button("Подтвердить 1 из 3", "restart-all-first-confirm")
	case s.restartStep == 1 && now.Before(s.restartUntil) && action == "restart-all-first-confirm":
		s.restartStep = 2
		panel.Text = "Работающие группы временно прервутся. После запуска оркестратор заново проверит эфиры и слоты."
		button("Подтвердить 2 из 3", "restart-all-second-confirm")
	case s.restartStep == 2 && now.Before(s.restartUntil) && action == "restart-all-second-confirm":
		s.restartStep = 3
		panel.Text = "Последнее подтверждение. Перезапустить оркестратор и все майнеры?"
		button("Перезапустить · 3 из 3", "restart-all-final-confirm")
	case s.restartStep == 3 && now.Before(s.restartUntil) && action == "restart-all-final-confirm" && s.restartRequests != nil:
		s.restartStep = 0
		panel.Text = "Перезапуск начнётся после доставки этого сообщения."
		panel.AfterRender = func() {
			select {
			case s.restartRequests <- struct{}{}:
			default:
			}
		}
		return panel
	default:
		panel.Text = "Подтверждение устарело. Начните заново из настроек."
		button("К настройкам", "settings")
		return panel
	}
	button("Отмена", "restart-all-cancel")
	return panel
}
