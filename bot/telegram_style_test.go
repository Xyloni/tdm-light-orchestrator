package bot

import (
	"strings"
	"testing"
)

func TestTelegramPanelEscapesDynamicContent(t *testing.T) {
	text := formatTelegramPanel("status", "", "Game <X> & Friends · пул 1/1\nРежим: <speed>")
	for _, want := range []string{"<b>📡 Состояние оркестратора</b>", "<b>Game &lt;X&gt; &amp; Friends · пул 1/1</b>", "<b>Режим:</b> &lt;speed&gt;"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestTelegramButtonsHaveConsistentEmojiLabels(t *testing.T) {
	for action, want := range map[string]string{"status": "📡", "resume:game": "▶️", "remove-delete-confirm:game": "⚠️", "export-remove-confirm:game:hash": "⚠️", "export-confirm:free:hash": "✅", "menu": "🏠", "prompt:timing": "🗓️", "prompt:simulate": "🧪", "resources": "🔁"} {
		if got := telegramButton(action, "label"); got != want+" label" {
			t.Errorf("%s: got %q, want %q", action, got, want+" label")
		}
	}
}

func TestTelegramScheduleHighlightsDatesAndEscapesGameName(t *testing.T) {
	text := formatTelegramPanel("game-preview:fixture", "", "📊 Нагрузка плана\nПик аккаунтов: 200 / 1000\n\n🎮 Game <X>\nСтарт: 10.11 в 09:00 MSK\nФиниш: 10.11 в 17:26 MSK\nРезерв кампании: 10 мин")
	for _, want := range []string{"<b>📊 Нагрузка плана</b>", "\n\n<b>🎮 Game &lt;X&gt;</b>\n", "<b>Старт:</b> 10.11 в 09:00 MSK\n<b>Финиш:</b> 10.11 в 17:26 MSK", "<b>Резерв кампании:</b> 10 мин"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}
