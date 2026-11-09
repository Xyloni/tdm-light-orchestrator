package bot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func uploadTestBot(t *testing.T, body string) (*Bot, *int, *int) {
	t.Helper()
	uploads, reloads := 0, 0
	b := New("test", 1, nil, nil, func() { reloads++ }, nil, nil, func(got []byte) error {
		if string(got) != body {
			t.Fatal("upload content changed")
		}
		uploads++
		return nil
	})
	b.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		content := `{"ok":true}`
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			content = `{"ok":true,"result":{"file_path":"documents/list.txt"}}`
		case strings.Contains(r.URL.Path, "/file/"):
			content = body
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(content)), Header: make(http.Header)}, nil
	})
	return b, &uploads, &reloads
}

func TestAccountsDocumentAnyTXTName(t *testing.T) {
	for _, name := range []string{"account.txt", "accounts.txt", "список аккаунтов.txt", "backup.2026.TXT", ".txt"} {
		t.Run(name, func(t *testing.T) {
			b, uploads, reloads := uploadTestBot(t, "e:login:p:token:1:device\n")
			b.handleAccountsDocument(context.Background(), "file", name, 0)
			if *uploads != 1 || *reloads != 1 {
				t.Fatalf("uploads=%d reloads=%d", *uploads, *reloads)
			}
		})
	}
}

func TestAccountsDocumentRejectsInvalidNamesAndSize(t *testing.T) {
	for _, name := range []string{"", "list.csv", "list.txt.exe", "../list.txt", `C:\list.txt`, "dir/list.txt"} {
		t.Run(name, func(t *testing.T) {
			b, uploads, reloads := uploadTestBot(t, "unused")
			b.handleAccountsDocument(context.Background(), "file", name, 1)
			if *uploads != 0 || *reloads != 0 {
				t.Fatal("invalid name accepted")
			}
		})
	}
	for _, size := range []int64{0, maxAccountsUploadBytes + 1} {
		b, uploads, reloads := uploadTestBot(t, strings.Repeat("x", maxAccountsUploadBytes+1))
		b.handleAccountsDocument(context.Background(), "file", "list.txt", size)
		if *uploads != 0 || *reloads != 0 {
			t.Fatal("oversized content accepted")
		}
	}
}

func TestAccountsDocumentValidationFailureDoesNotReload(t *testing.T) {
	b, _, reloads := uploadTestBot(t, "invalid account list")
	b.uploadFn = func([]byte) error { return errors.New("invalid account list") }
	b.handleAccountsDocument(context.Background(), "file", "backup.txt", 0)
	if *reloads != 0 {
		t.Fatal("failed validation triggered reload")
	}
}
