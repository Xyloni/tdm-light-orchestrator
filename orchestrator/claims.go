package orchestrator

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/twitch"
	"github.com/xyloni/tdm-light-orchestrator/worker"
)

type claimJob struct {
	running                                                     bool
	total, scanned, confirmed, already, failed, inventoryErrors int
}

func (s *scheduler) claimJobRunning(key string) bool {
	job := s.claimJobs[key]
	return job != nil && job.running
}

func (s *scheduler) startClaimJob(key string) string {
	g := s.state.Games[key]
	if g == nil || g.Removing {
		return "Игра недоступна."
	}
	if s.claimCtx == nil || s.claimCtx.Err() != nil {
		return "Оркестратор сейчас перезапускается."
	}
	if s.active[key] != nil || g.Regroup {
		return "Поставьте игру на паузу или дождитесь завершения её майнера."
	}
	for _, job := range s.claimJobs {
		if job.running {
			return "Дождитесь окончания текущей проверки наград."
		}
	}
	var accounts []account
	for _, a := range s.state.Accounts {
		if a.Game == key && !a.Quarantined {
			accounts = append(accounts, a)
		}
	}
	if len(accounts) == 0 {
		return "В пуле игры нет доступных аккаунтов."
	}
	if s.claimJobs == nil {
		s.claimJobs = make(map[string]*claimJob)
	}
	job := &claimJob{running: true, total: len(accounts)}
	s.claimJobs[key] = job
	go s.runClaimJob(key, g, s.configForGame(g), accounts, job)
	return "Проверка инвентарей запущена. Обновите экран, чтобы увидеть результат."
}

func (s *scheduler) runClaimJob(key string, g *gameState, cfg config.Config, accounts []account, job *claimJob) {
	ctx := s.claimCtx
	httpClient := &http.Client{Timeout: 12 * time.Second}
	defer httpClient.CloseIdleConnections()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		job.running = false
		if ctx.Err() == nil && s.statsDirty {
			s.persist()
		}
	}()
	for _, a := range accounts {
		s.mu.Lock()
		valid := s.state.Games[key] == g && !g.Removing
		current := slices.Contains(s.state.Accounts, a)
		s.mu.Unlock()
		if ctx.Err() != nil || !valid {
			return
		}
		if !current {
			s.mu.Lock()
			job.scanned++
			s.mu.Unlock()
			continue
		}
		client := twitch.NewGQLClient(httpClient, &cfg, &a.Credentials)
		drops, err := client.DropsDashboard(ctx)
		samples := make(map[string][]worker.ManagedRewardProgress)
		var confirmed, already, failed int
		for _, drop := range drops {
			if ctx.Err() != nil {
				return
			}
			if !strings.EqualFold(drop.GameName, g.Name) || drop.CampaignID == "" || drop.ID == "" ||
				drop.Required <= 0 || cfg.DropIgnored(drop.Name) {
				continue
			}
			sample := worker.ManagedRewardProgress{ID: drop.ID, Name: drop.Name, Earned: true, Claimed: drop.IsClaimed}
			switch {
			case drop.IsClaimed:
				already++
			case drop.ProgressKnown && drop.Current >= drop.Required:
				if _, err := client.ClaimAndVerifyDrop(ctx, drop); err != nil {
					sample.ClaimError = err.Error()
					failed++
				} else {
					sample.Claimed = true
					confirmed++
				}
			default:
				continue
			}
			samples[drop.CampaignID] = append(samples[drop.CampaignID], sample)
		}
		s.mu.Lock()
		if ctx.Err() != nil || s.state.Games[key] != g || g.Removing {
			s.mu.Unlock()
			return
		}
		job.scanned++
		job.confirmed += confirmed
		job.already += already
		job.failed += failed
		if err != nil {
			job.inventoryErrors++
		}
		for campaignID, rewards := range samples {
			if s.state.Stats.recordProgress(key, campaignID, []worker.ManagedProgress{{UserID: a.Credentials.UserID, RewardProgress: rewards}}, time.Now()) {
				s.statsDirty = true
			}
		}
		if s.statsDirty && job.scanned%20 == 0 {
			s.persist()
		}
		s.mu.Unlock()
	}
}

func (s *scheduler) claimSummary(key string) string {
	var lines []string
	if job := s.claimJobs[key]; job != nil {
		status := "Последняя проверка"
		if job.running {
			status = "Проверка выполняется"
		}
		lines = append(lines, fmt.Sprintf("%s: %d/%d аккаунтов\nПолучено сейчас: %d · уже получено: %d · не подтверждено: %d", status, job.scanned, job.total, job.confirmed, job.already, job.failed))
		if job.inventoryErrors > 0 {
			lines = append(lines, fmt.Sprintf("Не удалось прочитать инвентарь: %d аккаунтов", job.inventoryErrors))
		}
	}
	reasons := make(map[string]int)
	prefix := key + "\x00"
	for id, progress := range s.state.Stats.Progress {
		if strings.HasPrefix(id, prefix) && !progress.Claimed && progress.ClaimError != "" {
			reasons[progress.ClaimError]++
		}
	}
	if len(reasons) > 0 {
		var sorted []string
		for reason := range reasons {
			sorted = append(sorted, reason)
		}
		sort.Slice(sorted, func(i, j int) bool {
			if reasons[sorted[i]] != reasons[sorted[j]] {
				return reasons[sorted[i]] > reasons[sorted[j]]
			}
			return sorted[i] < sorted[j]
		})
		lines = append(lines, "⚠️ Получение на Twitch не подтверждено")
		for _, reason := range sorted[:min(4, len(sorted))] {
			lines = append(lines, fmt.Sprintf("• %d наград · %s", reasons[reason], brief(reason, 220)))
		}
		if len(sorted) > 4 {
			lines = append(lines, fmt.Sprintf("Других причин: %d", len(sorted)-4))
		}
	}
	return strings.Join(lines, "\n")
}

func (s *scheduler) telegramClaims(kind, ref string) bot.SettingsPanel {
	s.mu.Lock()
	defer s.mu.Unlock()
	panel := bot.SettingsPanel{Title: "🎁 Получение Drops"}
	row := func(label, action string) {
		panel.Buttons = append(panel.Buttons, []bot.SettingsButton{{Label: label, Action: action}})
	}
	for key, g := range s.state.Games {
		if gameRef(key) != ref || g.Removing {
			continue
		}
		panel.Title += " · " + g.Name
		if kind == "claim-scan-confirm" {
			panel.Text = s.startClaimJob(key) + "\n\n"
		}
		panel.Text += s.claimSummary(key)
		if strings.TrimSpace(panel.Text) == "" {
			panel.Text = "Ошибок получения пока не записано."
		}
		if !s.claimJobRunning(key) {
			panel.Text += "\n\nКнопка проверит инвентари пула и попробует забрать готовые награды этой игры, пока Twitch разрешает получение."
			row("Получить готовые Drops", "claim-scan-confirm:"+ref)
		}
		row("Обновить", "game-claims:"+ref)
		row("К игре", "game:"+ref)
		return panel
	}
	panel.Text = "Игра недоступна."
	row("К списку игр", "games")
	return panel
}
