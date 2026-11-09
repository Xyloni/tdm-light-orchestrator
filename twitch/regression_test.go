package twitch

import (
	"bufio"
	"context"
	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReviewWebSocketInvalidLengthMustNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("malformed WS frame panics: %v", r)
		}
	}()
	c := &wsConn{br: bufio.NewReader(strings.NewReader(string([]byte{0x81, 127, 255, 255, 255, 255, 255, 255, 255, 255})))}
	if _, err := c.readMessage(); err == nil {
		t.Fatal("invalid length accepted")
	}
}

func TestReviewConcurrentHeartbeats(t *testing.T) {
	h := &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		status := 200
		switch {
		case r.URL.Host == "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case r.Method == http.MethodPost:
			status = 204
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			body = "#EXTM3U\n#EXTINF:2,\nsegment.ts\n"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := NewGQLClient(h, config.Default(), &parser.Account{UserID: "1"})
			for j := 0; j < 20; j++ {
				_, err := c.WatchHeartbeat(context.Background(), "https://test.ttvnw.net/live.m3u8", Stream{Login: "test", ChannelID: "1", BroadcastID: "2", GameID: "3"})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
