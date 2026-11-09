package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
)

func (s *scheduler) searchCategories(ctx context.Context, query string) ([]string, error) {
	s.mu.Lock()
	var credentials []parser.Account
	for _, a := range s.state.Accounts {
		if a.Quarantined {
			continue
		}
		if len(credentials) == 3 {
			break
		}
		credentials = append(credentials, a.Credentials)
	}
	cfg := s.miner
	catalog := append([]string(nil), s.categoryNames...)
	s.mu.Unlock()
	if len(credentials) == 0 {
		return nil, fmt.Errorf("сначала загрузите хотя бы один Twitch-аккаунт в свободный пул")
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	cfg.Diagnostics = false
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	seen := make(map[string]bool)
	var names []string
	add := func(name string) {
		key := strings.ToLower(name)
		if !seen[key] {
			names = append(names, name)
			seen[key] = true
		}
	}
	found := false
	for _, acc := range credentials {
		results, err := twitch.NewGQLClient(client, &cfg, &acc).SearchCategories(ctx, query)
		if err == nil {
			for _, name := range results {
				add(name)
			}
			found = true
			break
		}
	}
	query = strings.ToLower(query)
	for _, name := range catalog {
		if strings.Contains(strings.ToLower(name), query) {
			add(name)
		}
	}
	if !found && len(names) == 0 {
		return nil, fmt.Errorf("Twitch не ответил на поиск категорий. Повторите попытку или введите полное название вручную")
	}
	sort.SliceStable(names, func(i, j int) bool {
		return strings.EqualFold(names[i], query) && !strings.EqualFold(names[j], query)
	})
	return names[:min(15, len(names))], nil
}
