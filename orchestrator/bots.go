package orchestrator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/xyloni/tdm-light-orchestrator/bot"
	"github.com/xyloni/tdm-light-orchestrator/config"
)

type telegramMenuState struct {
	Identity  string
	MessageID int64
}

func (s *scheduler) trackTelegramMenu(telegram *bot.Bot, entry botSettings) {
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(entry.OwnerID+"\x00"+entry.Token)))
	s.mu.Lock()
	last := s.state.TelegramMenu
	s.mu.Unlock()
	if last.Identity != identity {
		last.MessageID = 0
	}
	telegram.TrackOrchestratorMenu(last.MessageID, func(id int64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		previous := s.state.TelegramMenu
		s.state.TelegramMenu = telegramMenuState{Identity: identity, MessageID: id}
		if !s.persist() {
			s.state.TelegramMenu = previous
		}
	})
}

type botSettings struct {
	Platform string
	Token    string
	OwnerID  string
}

type botRuntime struct {
	mu       sync.RWMutex
	services []*bot.Service
}

func (b *botRuntime) notify(message string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, service := range b.services {
		service.Notify(message)
	}
}

func (b *botRuntime) publish(services []*bot.Service) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.services = services
}

func (s *scheduler) legacyBotSettings() []botSettings {
	var settings []botSettings
	if s.miner.TelegramBotToken != "" && s.miner.TelegramAdminID > 0 {
		settings = append(settings, botSettings{"telegram", s.miner.TelegramBotToken, strconv.FormatInt(s.miner.TelegramAdminID, 10)})
	}
	if s.miner.DiscordBotToken != "" && config.Snowflake(s.miner.DiscordAdminID) {
		settings = append(settings, botSettings{"discord", s.miner.DiscordBotToken, s.miner.DiscordAdminID})
	}
	return settings
}

func (s *scheduler) configuredBots() []botSettings {
	if s.state.Bot == nil {
		return s.legacyBotSettings()
	}
	if s.state.Bot.Platform == "none" {
		return nil
	}
	return []botSettings{*s.state.Bot}
}

func (s *scheduler) saveBot(platform, owner, token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token = strings.TrimSpace(token)
	owner = strings.TrimSpace(owner)
	next := botSettings{Platform: platform}
	switch platform {
	case "telegram":
		id, err := strconv.ParseInt(owner, 10, 64)
		if err != nil || id <= 0 {
			return "Укажите положительный числовой ID владельца Telegram."
		}
		next.OwnerID = strconv.FormatInt(id, 10)
	case "discord":
		if !config.Snowflake(owner) {
			return "Укажите числовой ID пользователя Discord."
		}
		next.OwnerID = owner
	case "none":
	default:
		return "Выберите Telegram, Discord или «Отключено»."
	}
	if platform != "none" {
		if token == "" {
			for _, existing := range s.configuredBots() {
				if existing.Platform == platform {
					token = existing.Token
					break
				}
			}
		}
		if token == "" || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
			return "Введите токен бота для выбранной платформы."
		}
		next.Token = token
	}
	old := s.state.Bot
	s.state.Bot = &next
	if !s.persist() {
		s.state.Bot = old
		return s.notice
	}
	select {
	case s.botChanges <- struct{}{}:
	default:
	}
	if platform == "none" {
		return "Управление через бота отключено."
	}
	return "Настройки бота сохранены. Оркестратор запускает подключение."
}

func (s *scheduler) runBots(ctx context.Context, runtime *botRuntime) {
	var previous []botSettings
	stop := func() {}
	defer func() { stop() }()
	for {
		s.mu.Lock()
		current := s.configuredBots()
		s.mu.Unlock()
		if !slices.Equal(current, previous) {
			runtime.publish(nil)
			stop()
			stop = s.startBotServices(ctx, runtime, current)
			previous = current
		}
		select {
		case <-ctx.Done():
			return
		case <-s.botChanges:
		}
	}
}

func (s *scheduler) startBotServices(ctx context.Context, runtime *botRuntime, current []botSettings) func() {
	serviceCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	var services []*bot.Service
	for _, entry := range current {
		var control bot.Control
		switch entry.Platform {
		case "telegram":
			id, err := strconv.ParseInt(entry.OwnerID, 10, 64)
			if err != nil || id <= 0 {
				log.Print("orchestrator: invalid saved Telegram owner ID")
				continue
			}
			telegram := bot.New(entry.Token, id, nil, nil, nil, nil, nil, s.upload)
			telegram.ConfigureOrchestrator(s.command)
			telegram.ConfigureOrchestratorMenu(s.telegramMenu, s.telegramUpload)
			s.trackTelegramMenu(telegram, entry)
			control = telegram
		case "discord":
			if !config.Snowflake(entry.OwnerID) {
				log.Print("orchestrator: invalid saved Discord owner ID")
				continue
			}
			control = bot.NewDiscord(entry.Token, entry.OwnerID, bot.Actions{Orchestrator: s.command, Upload: s.upload, Interactive: s.discordAction, ExportLogs: s.exportLogs})
		default:
			continue
		}
		service := bot.NewService(control)
		services = append(services, service)
		workers.Add(1)
		go func() { defer workers.Done(); service.Run(serviceCtx) }()
	}
	runtime.publish(services)
	return func() { cancel(); workers.Wait() }
}
