package orchestrator

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/bot"
)

func catalogAction(t *testing.T, panel bot.SettingsPanel, label string) string {
	t.Helper()
	for _, row := range panel.Buttons {
		for _, button := range row {
			if strings.Contains(strings.ToLower(button.Label), strings.ToLower(label)) {
				if len("control:"+button.Action) > 64 {
					t.Fatalf("callback exceeds Telegram's size limit: %q", button.Action)
				}
				return button.Action
			}
		}
	}
	t.Fatalf("button %q missing from %+v", label, panel)
	return ""
}

func addCatalogFixture(t *testing.T, s *scheduler, game, id, name string) {
	t.Helper()
	if err := s.saveManagedLotInput("lot-catalog", game+"\n"+id+" | "+name+"\nreward | Reward"); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramLotCatalogDeletesCampaignOrGame(t *testing.T) {
	s := testScheduler(t, time.Now())
	addCatalogFixture(t, s, "Game A", "one", "First campaign")
	addCatalogFixture(t, s, "game a", "two", "Second campaign")
	addCatalogFixture(t, s, "Game A", "three", "Third campaign")
	addCatalogFixture(t, s, "Game B", "other", "Other campaign")
	s.state.Games["game a"] = &gameState{Name: "Game A", Current: "one", Campaigns: map[string]*campaign{
		"one": {ID: "one", Name: "Farming campaign"},
	}}

	game := s.telegramMenu(catalogAction(t, s.telegramMenu("lot-catalog-page:0"), "Game A"))
	campaign := s.telegramMenu(catalogAction(t, game, "First campaign"))
	deleteCampaign := catalogAction(t, campaign, "Удалить кампанию из каталога")
	preview := s.telegramMenu(deleteCampaign)
	confirm := catalogAction(t, preview, "Подтвердить удаление")
	if len(s.state.LotCatalog) != 4 {
		t.Fatal("preview changed the catalog before confirmation")
	}
	s.telegramMenu(confirm)
	if len(s.state.LotCatalog) != 3 || s.state.LotCatalog["one"].Name != "" {
		t.Fatalf("wrong deletion scope: %+v", s.state.LotCatalog)
	}

	game = s.telegramMenu(catalogAction(t, s.telegramMenu("lot-catalog-page:0"), "Game A"))
	preview = s.telegramMenu(catalogAction(t, game, "Удалить игру из каталога"))
	s.telegramMenu(catalogAction(t, preview, "Подтвердить удаление"))
	state, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LotCatalog) != 1 || state.LotCatalog["other"].Game != "Game B" {
		t.Fatalf("unexpected catalog after reopening state: %+v", state.LotCatalog)
	}
	if game := state.Games["game a"]; game == nil || game.Current != "one" || game.Campaigns["one"] == nil {
		t.Fatalf("deleting from the lot catalog changed the farming game: %+v", game)
	}
}

func TestTelegramLotCatalogDeletionPreservesSavedBundle(t *testing.T) {
	s := testScheduler(t, time.Now())
	if err := s.saveManagedLotInput("lot-catalog", "Game A\none | First campaign\nfirst | First reward\nsecond | Second reward"); err != nil {
		t.Fatal(err)
	}
	campaign := s.state.LotCatalog["one"]
	campaign.Benefits = map[string][]string{"second": {"benefit"}}
	s.state.LotCatalog["one"] = campaign
	game := s.telegramMenu(catalogAction(t, s.telegramMenu("lot-catalog-page:0"), "Game A"))
	deleteGame := catalogAction(t, game, "Удалить игру из каталога")
	confirm := catalogAction(t, s.telegramMenu(deleteGame), "Подтвердить удаление")

	const ref = "0123456789abcdef01234567"
	const title = "Example listing"
	if err := s.registerLotTitle(ref, title, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.saveManagedLotInput("lot-bundle", ref+"\none second"); err != nil {
		t.Fatal(err)
	}
	savedBundle := s.state.ManagedLots[ref]
	if response := s.telegramMenu(confirm); !strings.Contains(response.Text, "Связанные лоты изменились") || len(s.state.LotCatalog) != 1 {
		t.Fatalf("old confirmation did not notice a new linked lot: %+v", response)
	}

	editor := s.telegramMenu("lb-open:" + ref)
	detail := s.telegramMenu(catalogAction(t, editor, "First campaign"))
	draft := s.telegramMenu(catalogAction(t, detail, "Снять выбор кампании"))
	if !strings.Contains(draft.Text, "0 из 2") || !reflect.DeepEqual(s.state.ManagedLots[ref], savedBundle) {
		t.Fatalf("clearing a draft should leave the saved bundle in place: %+v", draft)
	}

	preview := s.telegramMenu(deleteGame)
	if !strings.Contains(preview.Text, title) || !strings.Contains(preview.Text, "Сохранённый состав") {
		t.Fatalf("confirmation doesn't explain what happens to linked lots: %+v", preview)
	}
	s.telegramMenu(catalogAction(t, preview, "Подтвердить удаление"))
	state, err := loadState(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LotCatalog) != 0 || !reflect.DeepEqual(state.LotArchivedCampaigns["one"], campaign) ||
		!reflect.DeepEqual(state.ManagedLots[ref], savedBundle) {
		t.Fatalf("catalog removal changed its linked lot or lost campaign metadata: %+v", state.ManagedLots[ref])
	}
	if root := s.telegramMenu("lot-catalog-page:0"); !strings.Contains(root.Text, "Каталог пока пуст") {
		t.Fatalf("archived game still visible in catalog: %+v", root)
	}

	editor = s.telegramMenu("lb-open:" + ref)
	campaignAction := catalogAction(t, editor, "из состава лота")
	detail = s.telegramMenu(campaignAction)
	if !strings.Contains(detail.Text, "1 из 2") {
		t.Fatalf("editor did not restore saved partial selection: %+v", detail)
	}
	back := s.telegramMenu(catalogAction(t, detail, "К кампаниям"))
	review := s.telegramMenu(catalogAction(t, back, "Просмотреть и сохранить"))
	result := s.telegramMenu(catalogAction(t, review, "Сохранить состав"))
	if !strings.Contains(result.Text, "Состав сохранён") || !reflect.DeepEqual(s.state.ManagedLots[ref], savedBundle) {
		t.Fatalf("editing the preserved bundle changed its saved rewards: %+v", result)
	}

	const otherRef = "abcdef0123456789abcdef01"
	if err := s.saveManagedLotInput("lot-bundle", otherRef+"\none"); err == nil {
		t.Fatal("a different lot was able to use an archived campaign")
	}
}

func TestTelegramLotCatalogRejectsChangedGameAndReplayedConfirmation(t *testing.T) {
	s := testScheduler(t, time.Now())
	addCatalogFixture(t, s, "Game A", "one", "First campaign")
	game := s.telegramMenu(catalogAction(t, s.telegramMenu("lot-catalog-page:0"), "Game A"))
	deleteGame := catalogAction(t, game, "Удалить игру из каталога")
	preview := s.telegramMenu(deleteGame)
	confirm := catalogAction(t, preview, "Подтвердить удаление")
	s.telegramMenu(catalogAction(t, preview, "Отмена"))
	s.telegramMenu(confirm)
	if len(s.state.LotCatalog) != 1 {
		t.Fatal("confirmation was accepted after cancellation")
	}

	confirm = catalogAction(t, s.telegramMenu(deleteGame), "Подтвердить удаление")
	addCatalogFixture(t, s, "Game A", "two", "New campaign")
	s.telegramMenu(confirm)
	if len(s.state.LotCatalog) != 2 {
		t.Fatal("confirmation deleted a game whose campaigns changed after preview")
	}

	confirm = catalogAction(t, s.telegramMenu(deleteGame), "Подтвердить удаление")
	s.telegramMenu(confirm)
	if len(s.state.LotCatalog) != 0 {
		t.Fatal("fresh confirmation did not delete the game")
	}
	addCatalogFixture(t, s, "Game A", "one", "First campaign")
	addCatalogFixture(t, s, "Game A", "two", "New campaign")
	s.telegramMenu(confirm)
	if len(s.state.LotCatalog) != 2 {
		t.Fatal("confirmation was accepted a second time after the game was restored")
	}
}

func TestTelegramLotCatalogPreservesCampaignWhenSaveFails(t *testing.T) {
	s := testScheduler(t, time.Now())
	addCatalogFixture(t, s, "Game A", "one", "First campaign")
	game := s.telegramMenu(catalogAction(t, s.telegramMenu("lot-catalog-page:0"), "Game A"))
	preview := s.telegramMenu(catalogAction(t, game, "Удалить игру из каталога"))
	statePath := s.path
	s.path = filepath.Join(statePath, "cannot-save-under-file.json")
	s.telegramMenu(catalogAction(t, preview, "Подтвердить удаление"))
	state, err := loadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.LotCatalog["one"].Name == "" || s.state.LotCatalog["one"].Name == "" {
		t.Fatal("catalog entry was removed despite failed persistence")
	}
}
