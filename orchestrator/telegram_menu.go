package orchestrator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func gameRef(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *scheduler) telegramMenu(action string) bot.SettingsPanel {
	panel := bot.SettingsPanel{}
	button := func(label, key string) bot.SettingsButton { return bot.SettingsButton{Label: label, Action: key} }
	row := func(buttons ...bot.SettingsButton) { panel.Buttons = append(panel.Buttons, buttons) }
	if input, text, ok := strings.Cut(action, "\n"); ok {
		return s.telegramInput(input, text)
	}
	kind, exportValue, _ := strings.Cut(action, ":")
	switch kind {
	case "farm-campaign", "farm-pick":
		return s.telegramCampaignSelection(kind, exportValue)
	case "cov", "cov-sets", "cov-export", "cov-toggle", "cov-mode", "cov-page", "cov-confirm":
		return s.telegramCoveragePanel(kind, exportValue)
	case "help", "help-commands":
		return telegramHelp(exportValue, kind == "help-commands")
	case "logs":
		panel.Title = "📁 Дневные логи"
		now := time.Now()
		if exportValue == "" {
			panel.Text = "📅 Дата архива\nВыберите день по времени сервера.\n\n📦 Содержимое ZIP\nЖурнал оркестратора и логи майнеров."
		} else {
			name, data, message := s.exportLogs(context.Background(), exportValue)
			panel.Text = message
			if name != "" {
				panel.Document = &bot.TelegramDocument{Name: name, Data: data, Caption: "Архив дневных логов. Храните его приватно."}
			}
		}
		for days, label := range []string{"📅 Сегодня", "📅 Вчера", "📅 Позавчера"} {
			date := now.AddDate(0, 0, -days)
			row(button(label+" · "+date.Format("02.01"), "logs:"+date.Format(logDateLayout)))
		}
		row(button("В меню", "menu"))
		return panel
	case "export", "export-confirm", "export-clear-confirm", "export-remove-confirm":
		return s.telegramExport(kind, exportValue)
	}
	switch action {
	case "", "menu":
		panel.Title = "🎮  TDM LIGHT"
		s.mu.Lock()
		report := s.laggingReport("", time.Now())
		panel.Text = fmt.Sprintf("TWITCH DROPS  /  ЦЕНТР УПРАВЛЕНИЯ\n\n📡 Фарм сейчас\n🎮 Игры: %d / %d   ·   👥 Аккаунты: %d / %d", len(s.active), s.state.Settings.MaxGames, s.runningAccounts(), s.state.Settings.MaxAccounts)
		if report.Measured > 0 {
			panel.Text += fmt.Sprintf("\n⏱ Темп в норме: %d из %d с замером", report.Measured-report.ShortLow, report.Measured)
		} else {
			panel.Text += "\n⏱ Замеры темпа: пока нет данных"
		}
		s.mu.Unlock()
		panel.Text += "\n\nВыберите раздел управления ↓"
		row(button("Статус фарма", "status"), button("Темп начисления", "lagging"))
		row(button("Игры", "games"), button("Аккаунты", "accounts"))
		row(button("Статистика", "stats"), button("Уведомления", "notifications"))
		row(button("Нагрузка", "system-load"), button("📁 Скачать логи", "logs"))
		row(button("Настройки", "settings"), button("Справка", "help"))
		return panel
	case "quarantine":
		panel.Text = s.command("quarantine")
	case "accounts":
		panel.Text = "📥 Свободный пул\nОтправьте .txt документом в этот чат.\n\n🎮 Пул игры\nОткройте игру → Пул аккаунтов → Загрузить .txt."
		row(button("Закрепить аккаунт", "prompt:assign"), button("Вернуть в свободный пул", "prompt:free"))
		row(button("Очистить свободный пул", "clear-free"), button("Карантин", "quarantine"))
		row(button("Выгрузить свободный пул .txt", "export:free"))
	case "clear-free", "clear-free-confirm":
		s.mu.Lock()
		panel.Text = s.clearPool("free", "delete", action == "clear-free-confirm")
		if action == "clear-free" {
			_, count := s.poolFingerprint("")
			panel.Text = fmt.Sprintf("Удалить записи из свободного пула? Аккаунтов: %d.", count)
			row(button("Подтвердить очистку", "clear-free-confirm"))
			row(s.exportButton("Выгрузить .txt и удалить", "export-clear-confirm", "free", ""))
		}
		s.mu.Unlock()
		row(button("Аккаунты", "accounts"))
	case "notifications":
		s.mu.Lock()
		panel.Text = "🔔 Доставка\nНажмите на событие, чтобы переключить его.\n\n🕘 Суточная сводка\n" + s.setDigest("")
		row(button("Настроить суточную сводку", "prompt:digest"))
		if s.state.NotificationsMuted {
			row(button("Включить доставку", "notify:all:on"))
		} else {
			row(button("Приостановить все", "notify:all:off"))
		}
		for _, item := range noticeKinds {
			if s.noticeConfigured(item.Key) {
				row(button("✓ "+item.Label, "notify:"+item.Key+":off"))
			} else {
				row(button("○ "+item.Label, "notify:"+item.Key+":on"))
			}
		}
		s.mu.Unlock()
	case "preview-all", "preview-speed", "preview-resources":
		mode := "current"
		if action != "preview-all" {
			mode = strings.TrimPrefix(action, "preview-")
		}
		s.mu.Lock()
		panel.Text = s.previewSchedule("all", mode, time.Now())
		s.mu.Unlock()
		row(button("Скорость", "preview-speed"), button("Экономия", "preview-resources"))
		row(button("Перегруппировать по сохранённому режиму", "regroup-all"))
	case "regroup-all", "regroup-all-confirm":
		s.mu.Lock()
		panel.Text = s.requestRegroup("all", action == "regroup-all-confirm")
		s.mu.Unlock()
		if action == "regroup-all" {
			row(button("Подтвердить перегруппировку", "regroup-all-confirm"))
		}
	case "miner-options":
		s.mu.Lock()
		options := s.minerOptions()
		s.mu.Unlock()
		panel.Text = "🎛 Параметры майнеров\nИзменения вступят в силу со следующей группы."
		for _, option := range []struct {
			key, label string
			on         bool
		}{
			{"diagnostics", "Диагностика HTTP", options.Diagnostics},
			{"single_log", "Единый лог", options.SingleLog},
			{"pubsub", "События Twitch PubSub", options.UsePubSub},
		} {
			if option.on {
				row(button("✓ "+option.label, "miner-option:"+option.key+":off"))
			} else {
				row(button("○ "+option.label, "miner-option:"+option.key+":on"))
			}
		}
	case "stats":
		s.mu.Lock()
		panel.Text = s.telegramStats("", time.Now())
		s.mu.Unlock()
		row(button("Обновить", "stats"), button("Статус фарма", "status"))
	case "system-load":
		panel.Text = s.command("system-load")
		row(button("Обновить", "system-load"), button("Уведомления", "notifications"))
	case "lagging":
		s.mu.Lock()
		panel.Text = s.laggingReport("", time.Now()).text()
		s.mu.Unlock()
		row(button("По аккаунтам", "lagging-details"))
		row(button("Обновить", "lagging"), button("Статус фарма", "status"))
	case "lagging-details":
		s.mu.Lock()
		panel.Text = s.laggingReport("", time.Now()).details(10)
		s.mu.Unlock()
		row(button("Обновить", action), button("К сводке", "lagging"))
	case "status":
		panel.Text = s.telegramStatus(time.Now())
		row(button("Обновить", "status"), button("Темп начисления", "lagging"))
		row(button("Игры", "games"), button("Статистика", "stats"))
	case "settings", "speed", "resources", "trim", "trim-confirm":
		if input, text, ok := strings.Cut(action, "\n"); ok {
			return s.telegramInput(input, text)
		}
		kind, exportValue, _ := strings.Cut(action, ":")
		switch kind {
		case "export", "export-confirm", "export-clear-confirm", "export-remove-confirm":
			return s.telegramExport(kind, exportValue)
		}
		switch action {
		case "speed", "resources":
			panel.Text = s.command("mode "+action) + "\n"
		case "trim":
			panel.Text = s.command("trim") + "\n"
		case "trim-confirm":
			panel.Text = s.command("trim confirm") + "\n"
		}
		s.mu.Lock()
		cfg := s.state.Settings
		if panel.Text != "" {
			panel.Text += "\n"
		}
		panel.Text += fmt.Sprintf("⚙️ Текущие настройки\nРежим: %s\nЛимит: %d игр / %d аккаунтов\nРезерв кампании: %d мин\nСтартовая скорость: %d%%\nОпрос Twitch: %d сек.", cfg.modeName(), cfg.MaxGames, cfg.MaxAccounts, cfg.ReserveMinutes, cfg.ExpectedRatePercent, cfg.DiscoverySeconds)
		if s.state.Settings.SaveResources {
			row(button("Экономия → включить скорость", "speed"))
		} else {
			row(button("Скорость → включить экономию", "resources"))
		}
		s.mu.Unlock()
		row(button("Лимиты", "prompt:limits"), button("Расписание", "prompt:timing"))
		row(button("Уведомления", "notifications"), button("Параметры майнеров", "miner-options"))
		row(button("Предпросмотр всех игр", "preview-all"), button("Перегруппировать все игры", "regroup-all"))
		row(button("Симулятор расписания", "prompt:simulate"))
		row(button("Рассчитать снижение нагрузки", "trim"))
		if (action == "trim" || action == "trim-confirm") && strings.Contains(panel.Text, "Для подтверждения") {
			row(button("Подтвердить остановку", "trim-confirm"))
		}
	default:
		if value, ok := strings.CutPrefix(action, "find-choice:"); ok {
			return s.telegramSelectGame(value)
		}
		if value, ok := strings.CutPrefix(action, "miner-option:"); ok {
			key, setting, _ := strings.Cut(value, ":")
			message := s.command("miner " + key + " " + setting)
			panel = s.telegramMenu("miner-options")
			panel.Text = message + "\n\n" + panel.Text
			return panel
		}
		if value, ok := strings.CutPrefix(action, "notify:"); ok {
			key, setting, _ := strings.Cut(value, ":")
			message := s.command("notifications " + key + " " + setting)
			panel = s.telegramMenu("notifications")
			panel.Text = message + "\n\n" + panel.Text
			return panel
		}
		if value, ok := strings.CutPrefix(action, "prompt:"); ok {
			return s.inputPrompt(value)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		kind, value, _ := strings.Cut(action, ":")
		switch kind {
		case "games":
			keys := make([]string, 0, len(s.state.Games))
			for key := range s.state.Games {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			page, _ := strconv.Atoi(value)
			page = max(0, min(page, max(0, len(keys)-1)/8))
			panel.Text = fmt.Sprintf("Игр в списке: %d\n\nВыберите игру, чтобы открыть её состояние и действия.", len(keys))
			row(button("Найти игру в Twitch", "prompt:find"), button("Ввести точное название", "prompt:add"))
			if len(keys) == 0 {
				panel.Text = "Игры ещё не добавлены."
			}
			for _, key := range keys[page*8 : min(len(keys), (page+1)*8)] {
				row(button(s.state.Games[key].Name, "game:"+gameRef(key)))
			}
			if page > 0 {
				row(button("Предыдущие", fmt.Sprintf("games:%d", page-1)))
			}
			if (page+1)*8 < len(keys) {
				row(button("Следующие", fmt.Sprintf("games:%d", page+1)))
			}
		case "game", "game-pool", "game-options", "stop", "resume", "restart", "regroup", "regroup-confirm", "remove", "remove-release-confirm", "remove-delete-confirm", "clear-game", "clear-release-confirm", "clear-delete-confirm", "report", "game-quarantine", "game-stats", "game-lagging", "game-lagging-details", "game-preview", "game-preview-speed", "game-preview-resources":
			panel.Text = "Игра больше не найдена."
			for key, g := range s.state.Games {
				if gameRef(key) != value {
					continue
				}
				panel.Text = g.Name + "\n\n📡 Текущее состояние\n" + s.queueSummary(key, g, time.Now())
				switch kind {
				case "stop", "resume", "restart":
					result := s.controlGame(kind, key)
					panel.Text = g.Name + "\n\n" + result
				case "game-pool":
					panel.Title = "👥 Пул · " + g.Name
					_, count := s.poolFingerprint(key)
					panel.Text = fmt.Sprintf("Аккаунты: %d / %d\n\nЗдесь можно загрузить или выгрузить список и изменить целевой размер пула.", count, g.Target)
					row(button("Загрузить .txt", "prompt:upload:"+value), button("Выгрузить .txt", "export:"+value))
					row(button("Размер пула", "prompt:target:"+value), button("Карантин", "game-quarantine:"+value))
					row(button("Очистить пул", "clear-game:"+value))
					row(button("К игре", "game:"+value))
					return panel
				case "game-options":
					panel.Title = "⚙️ Настройки · " + g.Name
					cfg := s.gameSettings(g)
					panel.Text = fmt.Sprintf("Режим: %s\nРезерв кампании: %d мин\n\n%s", cfg.modeName(), cfg.ReserveMinutes, s.exclusionList(key))
					row(button("Правила игры", "prompt:policy:"+value))
					row(button("Исключить награду", "prompt:exclude:"+value), button("Вернуть награду", "prompt:include:"+value))
					row(button("Удалить игру", "remove:"+value))
					row(button("К игре", "game:"+value))
					return panel
				case "report":
					panel.Title = "📋 История кампаний · " + g.Name
					panel.Text = s.campaignReport(g)
					row(button("Статистика", "game-stats:"+value), button("К игре", "game:"+value))
					return panel
				case "game-preview", "game-preview-speed", "game-preview-resources":
					mode := "current"
					if kind != "game-preview" {
						mode = strings.TrimPrefix(kind, "game-preview-")
					}
					panel.Text = s.previewSchedule("game:"+key, mode, time.Now())
					row(button("Скорость", "game-preview-speed:"+value), button("Экономия", "game-preview-resources:"+value))
					row(button("К игре", "game:"+value))
					return panel
				case "game-lagging":
					panel.Title = "📊 Темп начисления · " + g.Name
					row(button("По аккаунтам", "game-lagging-details:"+value))
					panel.Text = s.laggingReport(key, time.Now()).text()
					row(button("Обновить", action), button("К игре", "game:"+value))
					return panel
				case "game-lagging-details":
					panel.Title = "👥 Темп по аккаунтам · " + g.Name
					panel.Text = s.laggingReport(key, time.Now()).details(10)
					row(button("Обновить", action), button("К сводке", "game-lagging:"+value))
					return panel
				case "game-stats":
					panel.Title = "📊 Статистика · " + g.Name
					panel.Text = s.telegramStats(key, time.Now())
					row(button("Обновить", action), button("К игре", "game:"+value))
					return panel
				case "game-quarantine":
					panel.Title = "🛡 Карантин · " + g.Name
					panel.Text = s.quarantineSummary(key)
					row(button("К пулу", "game-pool:"+value))
					return panel
				case "clear-game", "remove":
					_, count := s.poolFingerprint(key)
					prefix := "clear"
					panel.Text += fmt.Sprintf("\nВ пуле %d аккаунтов. Игра останется на паузе. Сначала остановите работающий майнер.", count)
					if kind == "remove" {
						prefix = "remove"
						panel.Text = fmt.Sprintf("%s\nБудут удалены игра и история кампаний. В пуле %d аккаунтов.", g.Name, count)
					}
					row(button("Вернуть аккаунты в свободный пул", prefix+"-release-confirm:"+value))
					row(button("Удалить записи аккаунтов", prefix+"-delete-confirm:"+value))
					row(s.exportButton("Выгрузить .txt и удалить", "export-"+prefix+"-confirm", value, key))
					row(button("Отмена", "game:"+value))
					return panel
				case "clear-release-confirm", "clear-delete-confirm", "remove-release-confirm", "remove-delete-confirm":
					disposition := "release"
					if strings.Contains(kind, "-delete-") {
						disposition = "delete"
					}
					if strings.HasPrefix(kind, "remove-") {
						panel.Text += "\n" + s.requestGameRemoval(key, disposition, true)
					} else {
						panel.Text += "\n" + s.clearPool("game:"+key, disposition, true)
					}
				case "regroup", "regroup-confirm":
					panel.Text += "\n" + s.requestRegroup("game:"+key, kind == "regroup-confirm")
					if kind == "regroup" {
						row(button("Подтвердить перегруппировку", "regroup-confirm:"+value))
					}
				}
				if s.state.Games[key] == nil {
					break
				}
				if g.Paused {
					panel.Text += "\nНа паузе."
					row(button("Возобновить", "resume:"+value))
				} else if !g.Removing {
					row(button("Остановить", "stop:"+value))
					if a := s.active[key]; a != nil && !a.stopping {
						row(button("Перезапустить майнер", "restart:"+value))
					}
				}
				if !g.Removing {
					row(button("Кампания для фарма", "farm-campaign:"+value))
					row(button("Пул аккаунтов", "game-pool:"+value), button("Настройки игры", "game-options:"+value))
					row(button("Статистика", "game-stats:"+value), button("История кампаний", "report:"+value))
					row(button("🏆 Кампании в пуле", "cov:"+value))
					row(button("Предпросмотр", "game-preview:"+value), button("Темп начисления", "game-lagging:"+value))
					if c := g.Campaigns[g.Current]; c != nil && c.Finished == "" && len(c.Group) > 0 {
						if !g.Paused && s.active[key] != nil && !s.active[key].stopping {
							row(button("Обновить", "game:"+value))
						}
						row(button("Перегруппировать", "regroup:"+value))
					}
				}
				break
			}
			row(button("К списку игр", "games"))
		default:
			panel.Text = "Действие недоступно."
		}
	}
	row(button("В меню", "menu"))
	return panel
}
