package twitch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

type categoryTransport func(*http.Request) (*http.Response, error)

func (f categoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchCategoriesUsesCanonicalNames(t *testing.T) {
	cfg := config.Default()
	client := &http.Client{Transport: categoryTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.twitch.tv" || r.URL.Path != "/helix/search/categories" || r.URL.Query().Get("query") != "Hitman" ||
			r.Header.Get("Client-Id") != cfg.ClientID || r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Fatalf("unexpected category request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"name":"HITMAN World of Assassination"}]}`))}, nil
	})}
	names, err := NewGQLClient(client, cfg, &parser.Account{OAuth: "fake-token"}).SearchCategories(context.Background(), "Hitman")
	if err != nil || len(names) != 1 || names[0] != "HITMAN World of Assassination" {
		t.Fatalf("unexpected result %v, %v", names, err)
	}
}
