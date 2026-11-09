package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestDiscordUploadAndClearRequireConfirmation(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["example"] = &gameState{Name: "Example"}
	data := []byte("mail:player:password:fake-token:1:device")
	message, choices := s.discordAction(context.Background(), "accounts", map[string]string{"game": "Example"}, data)
	if len(choices) != 1 || len(s.state.Accounts) != 0 || !strings.Contains(message, "Аккаунтов в файле: 1") || strings.Contains(message, "fake-token") {
		t.Fatal("upload preview must identify the pool without persisting credentials")
	}
	choices[0].Run()
	if len(s.state.Accounts) != 1 || s.state.Accounts[0].Game != "example" {
		t.Fatal("upload went to wrong pool")
	}
	_, choices = s.discordPoolAction("clear_pool", "example")
	if len(choices) != 2 {
		t.Fatal("expected both pool dispositions")
	}
	s.state.Accounts[0].Credentials.OAuth = "updated"
	if result := choices[1].Run(); !strings.Contains(result, "состав пула изменился") && !strings.Contains(result, "Состав пула изменился") {
		t.Fatal("stale confirmation accepted")
	}
	_, choices = s.discordPoolAction("clear_pool", "example")
	choices[0].Run()
	if len(s.state.Accounts) != 1 || s.state.Accounts[0].Game != "" || !s.state.Games["example"].Paused {
		t.Fatal("release must preserve credentials and pause the game")
	}
}

func TestTelegramRemovalCanDeleteAccountsWithoutTouchingOtherPools(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["example"] = &gameState{Name: "Example"}
	s.state.Accounts = []account{
		{Credentials: parser.Account{Login: "one"}, Game: "example"},
		{Credentials: parser.Account{Login: "two"}},
	}
	ref := gameRef("example")
	panel := s.telegramMenu("remove:" + ref)
	if len(panel.Buttons) < 2 || len(s.state.Accounts) != 2 || s.state.Games["example"] == nil {
		t.Fatal("removal preview should offer two choices without changing state")
	}
	s.telegramMenu("remove-delete-confirm:" + ref)
	saved, err := loadState(s.path)
	if err != nil || saved.Games["example"] != nil || len(saved.Accounts) != 1 || saved.Accounts[0].Credentials.Login != "two" {
		t.Fatalf("game deletion did not preserve the other pool: %v", err)
	}
}
