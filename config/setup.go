package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Setup runs only on a terminal (or explicitly via --setup). Services never wait
// for input. Existing complete Telegram configurations are adopted automatically.
func Setup(jsonPath, envPath string, in *os.File, out io.Writer, explicit bool) error {
	interactive := term.IsTerminal(int(in.Fd()))
	if !explicit && !interactive {
		return nil
	}
	cfg, err := LoadSettings(jsonPath, envPath)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	values := map[string]string{}
	ask := func(prompt string, secret bool) (string, error) {
		fmt.Fprint(out, prompt+": ")
		var value string
		if secret && interactive {
			b, e := term.ReadPassword(int(in.Fd()))
			fmt.Fprintln(out)
			value, err = string(b), e
		} else {
			value, err = reader.ReadString('\n')
		}
		if err != nil {
			return "", fmt.Errorf("настройка прервана: %w", err)
		}
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("значение не может быть пустым или содержать перевод строки")
		}
		return value, nil
	}
	platform := cfg.BotPlatform
	if platform == "" && cfg.Platform() == "telegram" {
		platform = "telegram"
	}
	if platform == "" {
		choice, e := ask("Выберите бота / Choose bot: 1 — Telegram, 2 — Discord", false)
		if e != nil {
			return e
		}
		switch strings.ToLower(choice) {
		case "1", "telegram", "tg":
			platform = "telegram"
		case "2", "discord", "ds":
			platform = "discord"
		default:
			return fmt.Errorf("выберите 1 или 2")
		}
	}
	if configured, ok := os.LookupEnv("BOT_PLATFORM"); ok && configured != "" && !strings.EqualFold(configured, platform) {
		return fmt.Errorf("BOT_PLATFORM задан в окружении; измените его там")
	}
	if cfg.BotPlatform != platform {
		values["BOT_PLATFORM"] = platform
	}
	switch platform {
	case "telegram":
		if cfg.TelegramBotToken == "" {
			v, e := ask("Telegram bot token (hidden)", true)
			if e != nil {
				return e
			}
			if !strings.Contains(v, ":") {
				return fmt.Errorf("неверный формат Telegram token")
			}
			values["TELEGRAM_BOT_TOKEN"] = v
		}
		if cfg.TelegramAdminID == 0 {
			v, e := ask("Ваш Telegram user ID", false)
			if e != nil {
				return e
			}
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n <= 0 {
				return fmt.Errorf("нужен положительный Telegram user ID")
			}
			values["TELEGRAM_ADMIN_ID"] = v
		}
	case "discord":
		if cfg.DiscordBotToken == "" {
			v, e := ask("Discord bot token (hidden)", true)
			if e != nil {
				return e
			}
			values["DISCORD_BOT_TOKEN"] = v
		}
		for _, field := range []struct{ key, value, prompt string }{
			{"DISCORD_ADMIN_ID", cfg.DiscordAdminID, "Ваш Discord user ID (Developer Mode → Copy User ID)"},
		} {
			if !Snowflake(field.value) {
				v, e := ask(field.prompt, false)
				if e != nil {
					return e
				}
				if !Snowflake(v) {
					return fmt.Errorf("%s: нужен числовой ID Discord", field.key)
				}
				values[field.key] = v
			}
		}
	}
	if strings.TrimSpace(cfg.TargetGame) == "" {
		v, e := ask("Целевая игра / Target game", false)
		if e != nil {
			return e
		}
		values["TARGET_GAME"] = v
	}
	if len(values) > 0 {
		for key, value := range values {
			if configured, ok := os.LookupEnv(key); ok && configured != value {
				return fmt.Errorf("%s задан в окружении и перекрывает config.env; измените или удалите эту переменную", key)
			}
		}
		if err := saveSettings(envPath, values); err != nil {
			return err
		}
	}
	if explicit {
		fmt.Fprintln(out, "Настройки сохранены / Settings saved: "+platform)
	}
	return nil
}

func Snowflake(value string) bool {
	if len(value) < 1 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, e := strconv.ParseUint(value, 10, 64)
	return e == nil && n > 0
}

func saveSettings(file string, values map[string]string) error {
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), "\n")
	remaining := make(map[string]string, len(values))
	for k, v := range values {
		remaining[k] = v
	}
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimPrefix(line, "\uFEFF"), "=")
		if value, exists := values[strings.TrimSpace(key)]; ok && exists {
			lines[i] = strings.TrimSpace(key) + "=" + value
			delete(remaining, strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"BOT_PLATFORM", "TELEGRAM_BOT_TOKEN", "TELEGRAM_ADMIN_ID", "DISCORD_BOT_TOKEN", "DISCORD_ADMIN_ID", "TARGET_GAME"} {
		if v, ok := remaining[key]; ok {
			lines = append(lines, key+"="+v)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".setup-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(strings.Join(lines, "\n") + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), file)
}
