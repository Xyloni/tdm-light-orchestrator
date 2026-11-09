package orchestrator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

type telegramSearchResult struct {
	ID      string
	Expires time.Time
	Target  int
	Names   []string
}

func (s *scheduler) telegramFindGame(input string) bot.SettingsPanel {
	count, query, _ := strings.Cut(strings.TrimSpace(input), " ")
	query = strings.TrimSpace(query)
	target, err := strconv.Atoi(count)
	if err != nil || target < 1 || len(query) < 2 || len(query) > 200 {
		panel := telegramPrompt("find")
		panel.Text = "Укажите положительный размер пула и часть названия.\n" + panel.Text
		return panel
	}
	names, err := s.searchCategories(context.Background(), query)
	panel := bot.SettingsPanel{Buttons: [][]bot.SettingsButton{{{Label: "К списку игр", Action: "games"}}}}
	if err != nil {
		panel.Text = err.Error()
		return panel
	}
	if len(names) == 0 {
		panel.Text = "Категории не найдены. Уточните название."
		return panel
	}
	now := time.Now()
	result := telegramSearchResult{ID: strconv.FormatInt(now.UnixNano(), 36), Expires: now.Add(2 * time.Minute), Target: target, Names: names}
	s.mu.Lock()
	s.telegramSearch = result
	s.mu.Unlock()
	panel.Text = fmt.Sprintf("Выберите точную категорию Twitch. Целевой пул: %d.", target)
	var choices [][]bot.SettingsButton
	for i, name := range names {
		choices = append(choices, []bot.SettingsButton{{Label: name, Action: fmt.Sprintf("find-choice:%s:%d", result.ID, i)}})
	}
	panel.Buttons = append(choices, panel.Buttons...)
	return panel
}

func (s *scheduler) telegramSelectGame(value string) string {
	id, index, _ := strings.Cut(value, ":")
	position, err := strconv.Atoi(index)
	s.mu.Lock()
	result := s.telegramSearch
	if err != nil || position < 0 || position >= len(result.Names) || id != result.ID || !time.Now().Before(result.Expires) {
		s.mu.Unlock()
		return "Результаты поиска устарели. Найдите игру заново."
	}
	s.telegramSearch = telegramSearchResult{}
	s.mu.Unlock()
	return s.operate("add", strconv.Itoa(result.Target)+" "+result.Names[position])
}
