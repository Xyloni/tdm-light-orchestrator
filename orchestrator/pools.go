package orchestrator

import (
	"fmt"
	"strings"
)

func (s *scheduler) clearPool(scope, disposition string, confirmed bool) string {
	if !confirmed {
		return "Подтвердите очистку выбранного пула."
	}
	switch {
	case scope == "free":
		if disposition != "delete" {
			return "Для свободного пула выберите удаление записей."
		}
		return s.clearFreePool()
	case strings.HasPrefix(scope, "game:"):
		if disposition != "release" && disposition != "delete" {
			return "Выберите, удалить аккаунты или вернуть их в свободный пул."
		}
		return s.clearGamePool(strings.TrimPrefix(scope, "game:"), disposition == "delete")
	default:
		return "Выберите пул для очистки."
	}
}

func (s *scheduler) clearFreePool() string {
	old := s.state.Accounts
	kept := make([]account, 0, len(old))
	for _, a := range old {
		if a.Game != "" {
			kept = append(kept, a)
		}
	}
	removed := len(old) - len(kept)
	if removed == 0 {
		return "Свободный пул уже пуст."
	}
	s.state.Accounts = kept
	if !s.persist() {
		s.state.Accounts = old
		return s.notice
	}
	return fmt.Sprintf("Из свободного пула удалено аккаунтов: %d.", removed)
}

func (s *scheduler) clearGamePool(key string, deleteAccounts bool) string {
	g := s.state.Games[key]
	if g == nil {
		return "Игра не найдена."
	}
	if g.Removing || g.Regroup {
		return "Дождитесь завершения операции с игрой."
	}
	if s.active[key] != nil {
		return "Сначала остановите игру и дождитесь завершения её майнера."
	}

	old := s.state.Accounts
	accounts, released := changePoolMembership(old, key, deleteAccounts)
	if released == 0 {
		return "Пул игры уже пуст."
	}

	paused := g.Paused
	g.Paused = true
	current := g.Campaigns[g.Current]
	var group []string
	if current != nil {
		group = current.Group
		current.Group = nil
	}
	s.state.Accounts = accounts
	if !s.persist() {
		s.state.Accounts = old
		g.Paused = paused
		if current != nil {
			current.Group = group
		}
		return s.notice
	}
	if deleteAccounts {
		return fmt.Sprintf("«%s» на паузе. Удалено аккаунтов: %d.", g.Name, released)
	}
	return fmt.Sprintf("«%s» на паузе. В свободный пул возвращено аккаунтов: %d.", g.Name, released)
}

func changePoolMembership(old []account, key string, deleteAccounts bool) ([]account, int) {
	accounts := make([]account, 0, len(old))
	count := 0
	for _, a := range old {
		if a.Game == key {
			count++
			if deleteAccounts {
				continue
			}
			a.Game = ""
		}
		accounts = append(accounts, a)
	}
	return accounts, count
}
