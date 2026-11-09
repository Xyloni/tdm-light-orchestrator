package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type documentTransport func(*http.Request) (*http.Response, error)

func (f documentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExportRunsDeletionOnlyAfterTelegramAcceptsFile(t *testing.T) {
	for _, reply := range []struct {
		body     string
		accepted bool
	}{
		{`{"ok":false}`, false},
		{`{"ok":true}`, true},
	} {
		b := New("fake-bot-token", 123, nil, nil, nil, nil, nil, nil)
		b.http = &http.Client{Transport: documentTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.telegram.org" || !strings.HasSuffix(r.URL.Path, "/sendDocument") {
				t.Fatal("wrong Telegram endpoint")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("chat_id") != "123" {
				t.Fatal("document must go to the configured owner")
			}
			file, header, err := r.FormFile("document")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			content, err := io.ReadAll(file)
			if err != nil || header.Filename != "accounts-free.txt" || string(content) != "test-data" {
				t.Fatal("incorrect document")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
		})}
		deleted := false
		b.sendOrchestratorDocument(context.Background(), &TelegramDocument{Name: "accounts-free.txt", Data: []byte("test-data"), AfterSend: func() string { deleted = true; return "Удалено." }})
		if deleted != reply.accepted {
			t.Fatalf("deleted=%t for Telegram accepted=%t", deleted, reply.accepted)
		}
	}
}
