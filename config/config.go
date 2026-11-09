package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config — вся конфигурация сервиса.
type Config struct {
	CampaignID              string   `json:"campaign_id,omitempty"`
	DailyLogs               bool     `json:"-"`
	ManagedAuthAttempts     int      `json:"-"`
	StopWhenIdle            bool     `json:"stop_when_idle"`
	IdleCheckRounds         int      `json:"idle_check_rounds"`
	SingleLog               bool     `json:"single_log"`
	Diagnostics             bool     `json:"diagnostics"`
	BotPlatform             string   `json:"bot_platform"`
	DiscordBotToken         string   `json:"discord_bot_token"`
	DiscordAdminID          string   `json:"discord_admin_id"`
	TargetGame              string   `json:"target_game"` // ключевая игра для фарма
	IgnoredDropNames        []string `json:"ignored_drop_names"`
	ignoredDropParseErr     error
	ClientID                string `json:"client_id"`                  // Client-ID клиента, которым выданы OAuth-токены
	TelegramBotToken        string `json:"telegram_bot_token"`         // токен TG-бота
	TelegramAdminID         int64  `json:"telegram_admin_id"`          // ID администратора (chat_id)
	AccountsFile            string `json:"accounts_file"`              // путь к account.txt
	HeartbeatIntervalSec    int    `json:"heartbeat_interval_sec"`     // интервал beacon, минимум 60 секунд; HLS отдельно каждые 20 секунд
	CampaignPollIntervalSec int    `json:"campaign_poll_interval_sec"` // интервал опроса прогресса дропов
	JitterSec               int    `json:"jitter_sec"`                 // ± секунд случайного разброса
	GroupSessionMinutes     int    `json:"group_session_minutes"`      // устаревшая настройка: читается для предупреждения, таймер групп отключён
	LogDir                  string `json:"log_dir"`                    // каталог текстовых логов аккаунтов
	UsePubSub               bool   `json:"use_pubsub"`                 // опциональный WebSocket PubSub для событий дропов
}

// Default возвращает конфигурацию с безопасными значениями по умолчанию.
func Default() *Config {
	return &Config{
		SingleLog:               true,
		IdleCheckRounds:         3,
		IgnoredDropNames:        []string{"Hunt White"},
		ClientID:                "kd1unb4b3q4t58fwlpcbzcbnm76a8fp", // Client-ID приложения Twitch для Android
		AccountsFile:            "account.txt",
		HeartbeatIntervalSec:    60,
		CampaignPollIntervalSec: 120,
		JitterSec:               20,
		LogDir:                  "logs",
	}
}

// Load читает config.json (если существует), затем применяет переопределения
// из config.env и переменных окружения (приоритет: env > config.env > config.json).
func Load(jsonPath, envPath string) (*Config, error) {
	cfg, err := LoadSettings(jsonPath, envPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.TargetGame) == "" {
		return nil, fmt.Errorf("target_game не задан ни в config.json, ни в окружении")
	}
	return cfg, nil
}

func LoadSettings(jsonPath, envPath string) (*Config, error) {
	cfg := Default()

	if data, err := os.ReadFile(jsonPath); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", jsonPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("%s: %w", jsonPath, err)
	}

	applyEnvFile(cfg, envPath)
	applyOSEnv(cfg)
	if cfg.IdleCheckRounds < 1 || cfg.IdleCheckRounds > 100 {
		return nil, fmt.Errorf("IDLE_CHECK_ROUNDS: используйте число от 1 до 100")
	}
	if cfg.ignoredDropParseErr != nil {
		return nil, cfg.ignoredDropParseErr
	}
	// Normalize the previous conventional filename, including absolute Docker paths.
	if filepath.Base(cfg.AccountsFile) == "accounts.txt" {
		cfg.AccountsFile = filepath.Join(filepath.Dir(cfg.AccountsFile), "account.txt")
	}

	cfg.BotPlatform = strings.ToLower(strings.TrimSpace(cfg.BotPlatform))
	if cfg.BotPlatform != "" && cfg.BotPlatform != "telegram" && cfg.BotPlatform != "discord" && cfg.BotPlatform != "none" {
		return nil, fmt.Errorf("BOT_PLATFORM: используйте telegram, discord или none")
	}
	return cfg, nil
}

// Platform preserves existing Telegram installations without changing their config.
func (c *Config) Platform() string {
	if c.BotPlatform != "" {
		return c.BotPlatform
	}
	if c.TelegramBotToken != "" && c.TelegramAdminID != 0 {
		return "telegram"
	}
	return "none"
}

// applyEnvFile разбирает простой KEY=VALUE файл.
func applyEnvFile(cfg *Config, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		applyKV(cfg, strings.TrimSpace(k), strings.TrimSpace(v))
	}
}

func applyOSEnv(cfg *Config) {
	for _, k := range []string{
		"BOT_PLATFORM", "DISCORD_BOT_TOKEN", "DISCORD_ADMIN_ID",
		"TARGET_GAME", "CLIENT_ID", "TELEGRAM_BOT_TOKEN", "TELEGRAM_ADMIN_ID",
		"IGNORED_DROP_NAMES", "STOP_WHEN_IDLE", "IDLE_CHECK_ROUNDS",
		"ACCOUNTS_FILE", "HEARTBEAT_INTERVAL_SEC", "CAMPAIGN_POLL_INTERVAL_SEC",
		"JITTER_SEC", "GROUP_SESSION_MINUTES", "LOG_DIR", "USE_PUBSUB", "SINGLE_LOG", "DIAGNOSTICS",
	} {
		if v, ok := os.LookupEnv(k); ok {
			applyKV(cfg, k, v)
		}
	}
}

func applyKV(cfg *Config, key, val string) {
	switch strings.ToUpper(key) {
	case "STOP_WHEN_IDLE":
		cfg.StopWhenIdle = val == "1" || strings.EqualFold(val, "true")
	case "IDLE_CHECK_ROUNDS":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.IdleCheckRounds = n
		}
	case "BOT_PLATFORM":
		cfg.BotPlatform = val
	case "DISCORD_BOT_TOKEN":
		cfg.DiscordBotToken = val
	case "DISCORD_ADMIN_ID":
		cfg.DiscordAdminID = val
	case "TARGET_GAME":
		cfg.TargetGame = val
	case "IGNORED_DROP_NAMES":
		cfg.ignoredDropParseErr = nil
		if strings.HasPrefix(val, "[") {
			var names []string
			if json.Unmarshal([]byte(val), &names) == nil {
				cfg.IgnoredDropNames = names
			} else {
				cfg.ignoredDropParseErr = fmt.Errorf("IGNORED_DROP_NAMES: неверный JSON-массив названий")
			}
		} else {
			cfg.IgnoredDropNames = nil
			for _, name := range strings.Split(val, ",") {
				if name = strings.TrimSpace(name); name != "" {
					cfg.IgnoredDropNames = append(cfg.IgnoredDropNames, name)
				}
			}
		}
	case "CLIENT_ID":
		cfg.ClientID = val
	case "TELEGRAM_BOT_TOKEN":
		cfg.TelegramBotToken = val
	case "TELEGRAM_ADMIN_ID":
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			cfg.TelegramAdminID = n
		}
	case "ACCOUNTS_FILE":
		cfg.AccountsFile = val
	case "HEARTBEAT_INTERVAL_SEC":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.HeartbeatIntervalSec = n
		}
	case "CAMPAIGN_POLL_INTERVAL_SEC":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.CampaignPollIntervalSec = n
		}
	case "JITTER_SEC":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.JitterSec = n
		}
	case "GROUP_SESSION_MINUTES":
		if n, err := strconv.Atoi(val); err == nil {
			cfg.GroupSessionMinutes = n
		}
	case "SINGLE_LOG":
		cfg.SingleLog = val == "1" || strings.EqualFold(val, "true")
	case "DIAGNOSTICS":
		cfg.Diagnostics = val == "1" || strings.EqualFold(val, "true")
	case "LOG_DIR":
		cfg.LogDir = val
	case "USE_PUBSUB":
		cfg.UsePubSub = val == "1" || strings.EqualFold(val, "true")
	}
}

func (c *Config) DropIgnored(name string) bool {
	for _, ignored := range c.IgnoredDropNames {
		if strings.TrimSpace(ignored) != "" && strings.EqualFold(strings.TrimSpace(ignored), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}
