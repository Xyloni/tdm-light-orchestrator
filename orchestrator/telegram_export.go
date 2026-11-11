package orchestrator

import (
	"fmt"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func (s *scheduler) exportButton(label, kind, ref, key string) bot.SettingsButton {
	fingerprint, _ := s.poolFingerprint(key)
	return bot.SettingsButton{Label: label, Action: kind + ":" + ref + ":" + fingerprint[:12]}
}

func (s *scheduler) telegramExport(kind, value string) bot.SettingsPanel {
	panel := bot.SettingsPanel{Buttons: [][]bot.SettingsButton{{{Label: "Аккаунты", Action: "accounts"}, {Label: "В меню", Action: "menu"}}}}
	ref, preview, _ := strings.Cut(value, ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	key, label := "", "Свободный пул"
	var game *gameState
	if ref != "free" {
		for candidate, g := range s.state.Games {
			if gameRef(candidate) == ref && !g.Removing {
				key, game, label = candidate, g, g.Name
				break
			}
		}
		if game == nil {
			panel.Text = "Игра не найдена или удаляется."
			return panel
		}
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: "К игре", Action: "game:" + ref}})
	}
	fingerprint, count := s.poolFingerprint(key)
	if count == 0 {
		panel.Text = "В выбранном пуле нет аккаунтов."
		return panel
	}
	if kind == "export" {
		panel.Text = fmt.Sprintf("Выгрузить .txt из пула «%s»? Аккаунтов: %d. Файл содержит учётные данные Twitch.", label, count)
		panel.Buttons = append([][]bot.SettingsButton{{s.exportButton("Подтвердить выгрузку", "export-confirm", ref, key)}}, panel.Buttons...)
		return panel
	}
	if preview != fingerprint[:12] {
		panel.Text = "Состав пула изменился. Откройте действие заново."
		return panel
	}
	if kind == "export-clear-confirm" && game != nil && (s.active[key] != nil || game.Regroup) {
		panel.Text = "Сначала остановите игру и дождитесь окончания операции с её майнером."
		return panel
	}
	if kind == "export-remove-confirm" && game == nil {
		panel.Text = "Выберите игру."
		return panel
	}
	var accounts []account
	for _, a := range s.state.Accounts {
		if a.Game == key {
			accounts = append(accounts, a)
		}
	}
	content, err := encodeAccountFile(accounts)
	if err != nil {
		panel.Text = "Не удалось собрать файл. " + err.Error() + ". Операция отменена."
		return panel
	}
	doc := &bot.TelegramDocument{Name: accountExportName(label), Data: content}
	if kind == "export-clear-confirm" || kind == "export-remove-confirm" {
		doc.AfterSend = func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			current, _ := s.poolFingerprint(key)
			if current != fingerprint || (game != nil && s.state.Games[key] != game) {
				return "Удаление отменено: пул изменился во время отправки."
			}
			if kind == "export-remove-confirm" {
				return s.requestGameRemoval(key, "delete", true)
			}
			scope := "free"
			if game != nil {
				scope = "game:" + key
			}
			return s.clearPool(scope, "delete", true)
		}
	}
	panel.Document = doc
	return panel
}
