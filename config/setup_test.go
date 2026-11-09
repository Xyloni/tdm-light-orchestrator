package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLaunchChoicePersists(t *testing.T) {
	for _, platform := range []string{"telegram", "discord"} {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			jsonFile := filepath.Join(dir, "missing.json")
			env := filepath.Join(dir, "config.env")
			input := "1\n123456:token\n123\nGame\n"
			if platform == "discord" {
				input = "2\nsecret-token\n123\nGame\n"
			}
			inputFile := filepath.Join(dir, "input")
			if err := os.WriteFile(inputFile, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(inputFile)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var output bytes.Buffer
			if err := Setup(jsonFile, env, f, &output, true); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(jsonFile, env)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Platform() != platform || cfg.TargetGame != "Game" {
				t.Fatal("settings lost")
			}
			if strings.Contains(output.String(), "secret-token") || strings.Contains(output.String(), "123456:token") {
				t.Fatal("token displayed")
			}
			// A second setup must require no input.
			if err := Setup(jsonFile, env, f, &output, true); err != nil {
				t.Fatal("repeated prompt", err)
			}
		})
	}
}

func TestLegacyAndHeadlessSetup(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "config.env")
	jsonFile := filepath.Join(dir, "missing.json")
	if err := os.WriteFile(env, []byte("TELEGRAM_BOT_TOKEN=existing\nTELEGRAM_ADMIN_ID=123\nTARGET_GAME=Game\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, e := os.Open(os.DevNull)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if err := Setup(jsonFile, env, f, &bytes.Buffer{}, true); err != nil {
		t.Fatal("legacy setup prompted", err)
	}
	cfg, err := Load(jsonFile, env)
	if err != nil || cfg.BotPlatform != "telegram" {
		t.Fatal("legacy platform lost", err)
	}
	if err := Setup(jsonFile, filepath.Join(dir, "absent.env"), f, &bytes.Buffer{}, false); err != nil {
		t.Fatal("headless attempted interaction", err)
	}
}

func TestPlatformEnvironmentPriority(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "config.env")
	_ = os.WriteFile(env, []byte("BOT_PLATFORM=telegram\nTARGET_GAME=Game\nDISCORD_ADMIN_ID=111\n"), 0600)
	t.Setenv("BOT_PLATFORM", "discord")
	t.Setenv("DISCORD_ADMIN_ID", "222")
	cfg, err := Load(filepath.Join(dir, "absent.json"), env)
	if err != nil || cfg.Platform() != "discord" || cfg.DiscordAdminID != "222" {
		t.Fatal("environment priority", err)
	}
	t.Setenv("BOT_PLATFORM", "invalid")
	if _, err := Load("", env); err == nil {
		t.Fatal("invalid platform accepted")
	}
}

func TestSetupRejectsOverriddenCredentials(t *testing.T) {
	dir := t.TempDir()
	jsonFile := filepath.Join(dir, "missing.json")
	env := filepath.Join(dir, "config.env")
	_ = os.WriteFile(env, []byte("BOT_PLATFORM=discord\nDISCORD_ADMIN_ID=123\nDISCORD_CHANNEL_ID=456\nTARGET_GAME=Game\n"), 0600)
	t.Setenv("DISCORD_BOT_TOKEN", "")
	input := filepath.Join(dir, "input")
	_ = os.WriteFile(input, []byte("test-token\n"), 0600)
	f, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = Setup(jsonFile, env, f, &bytes.Buffer{}, true)
	if err == nil || !strings.Contains(err.Error(), "DISCORD_BOT_TOKEN") {
		t.Fatal("false success with an environment override", err)
	}
	data, _ := os.ReadFile(env)
	if strings.Contains(string(data), "test-token") {
		t.Fatal("failed setup persisted token")
	}
}
