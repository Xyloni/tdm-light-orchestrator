package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestTelegramExportAndDeleteUsesConfirmedSnapshot(t *testing.T) {
	s := testScheduler(t, time.Now())
	s.state.Games["example"] = &gameState{Name: "Example"}
	s.state.Accounts = []account{
		{Game: "example", Credentials: parser.Account{Login: "player", Password: "pass", UserID: "1", OAuth: "token"}},
		{Credentials: parser.Account{Login: "free", UserID: "2", OAuth: "token2"}},
	}
	ref := gameRef("example")
	preview := s.telegramMenu("remove:" + ref)
	var action string
	for _, row := range preview.Buttons {
		for _, button := range row {
			if strings.HasPrefix(button.Action, "export-remove-confirm:") {
				action = button.Action
			}
		}
	}
	if action == "" || preview.Document != nil || len("control:"+action) > 64 {
		t.Fatal("expected confirmation button before generating the document")
	}
	panel := s.telegramMenu(action)
	if panel.Document == nil || s.state.Games["example"] == nil {
		t.Fatal("game removed before file delivery")
	}
	groups, err := parser.Parse(strings.NewReader(string(panel.Document.Data)))
	if err != nil || len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].Login != "player" || groups[0][0].OAuth != "token" {
		t.Fatalf("expected an importable export scoped to the selected pool: %v", err)
	}
	s.state.Accounts[0].Credentials.Password = "changed"
	if result := panel.Document.AfterSend(); !strings.Contains(result, "отменено") || s.state.Games["example"] == nil {
		t.Fatal("deletion must refuse stale data")
	}
	action = s.exportButton("export", "export-remove-confirm", ref, "example").Action
	panel = s.telegramMenu(action)
	panel.Document.AfterSend()
	if s.state.Games["example"] != nil || len(s.state.Accounts) != 1 || s.state.Accounts[0].Credentials.Login != "free" {
		t.Fatal("deletion must affect only the selected game and its accounts")
	}
}
