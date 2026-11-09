package orchestrator

import (
	"fmt"
	"strings"

	"github.com/xyloni/tdm-light-orchestrator/worker"
)

type noticeKind struct {
	Key, Label string
	Default    bool
}

var noticeKinds = []noticeKind{
	{"catalog", "Каталог Twitch: сбой и восстановление", true},
	{"capacity", "Превышение лимита аккаунтов", true},
	{"campaign_done", "Итог кампании", true},
	{"campaign_new", "Новая кампания", false},
	{"group_start", "Запуск группы", false},
	{"group_done", "Завершение группы", false},
	{"stalled", "Минуты не начисляются у большинства группы", false},
	{"auth", "Ошибки авторизации и карантин", true},
	{"reward", "Готовность отдельных наград", false},
}

func (s *scheduler) noticeEnabled(kind string) bool {
	return !s.state.NotificationsMuted && s.noticeConfigured(kind)
}

func (s *scheduler) noticeConfigured(kind string) bool {

	if on, ok := s.state.NotificationOverrides[kind]; ok {
		return on
	}
	for _, item := range noticeKinds {
		if item.Key == kind {
			return item.Default
		}
	}
	return false
}

func (s *scheduler) sendNotice(kind, message string) {
	if s.notify != nil && s.noticeEnabled(kind) {
		s.notify(message)
	}
}

func (s *scheduler) notificationSetting(key, value string) string {
	if value != "on" && value != "off" {
		return "Укажите on или off."
	}
	on := value == "on"
	if key == "all" {
		previous := s.state.NotificationsMuted
		s.state.NotificationsMuted = !on
		if !s.persist() {
			s.state.NotificationsMuted = previous
			return s.notice
		}
		return "Общий переключатель уведомлений сохранён."
	}
	for _, item := range noticeKinds {
		if item.Key != key {
			continue
		}
		if s.state.NotificationOverrides == nil {
			s.state.NotificationOverrides = make(map[string]bool)
		}
		old, had := s.state.NotificationOverrides[key]
		s.state.NotificationOverrides[key] = on
		if !s.persist() {
			if had {
				s.state.NotificationOverrides[key] = old
			} else {
				delete(s.state.NotificationOverrides, key)
			}
			return s.notice
		}
		return "Сохранено: " + item.Label + "."
	}
	return "Неизвестный тип уведомления."
}

func (s *scheduler) observeGroupNotices(a *activeGame, g *gameState, progress []worker.ManagedProgress) {
	if a.authNotified == nil {
		a.authNotified = make(map[string]bool)
	}
	var names []string
	active, zero, positive := 0, 0, 0
	for _, p := range progress {
		if p.Status == "error: auth" && !a.authNotified[p.Login] {
			a.authNotified[p.Login] = true
			names = append(names, p.Login)
		}
		if terminal(p.Status) {
			continue
		}
		active++
		if p.RateKnown {
			if p.Rate == 0 {
				zero++
			} else if p.Rate > 0 {
				positive++
			}
		}
	}
	if len(names) > 0 {
		if s.quarantineAccounts(names) {
			s.persist()
		}
		s.sendNotice("auth", fmt.Sprintf("«%s»: ошибок авторизации %d. Аккаунты в карантине: %s. Полный список: /quarantine.", g.Name, len(names), strings.Join(names[:min(8, len(names))], ", ")))
	}
	if active == 0 || positive > active/2 {
		a.stallNotified = false
	}
	if zero > active/2 && !a.stallNotified {
		a.stallNotified = true
		s.sendNotice("stalled", fmt.Sprintf("«%s»: у %d из %d работающих аккаунтов не росли минуты в последнем окне замеров Twitch Inventory.", g.Name, zero, active))
	}
}
