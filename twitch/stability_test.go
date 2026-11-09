package twitch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestReview500AccountsDelayedAndBrokenConnections(t *testing.T) {
	var broken sync.Map
	h := &http.Client{Timeout: 5 * time.Second, Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		timer := time.NewTimer(time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
		status, body := 200, ""
		switch {
		case r.URL.Host == "gql.twitch.tv":
			body = `{"data":{"streamPlaybackAccessToken":{"value":"test","signature":"test"}}}`
		case r.URL.Host == "usher.ttvnw.net":
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/channel/hls/"), ".m3u8")
			body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://test.ttvnw.net/" + name + ".m3u8\n"
		case r.URL.Host == "www.twitch.tv":
			body = `{"spade_url":"https://spade.twitch.tv/track"}`
		case r.Method == http.MethodPost:
			status = 204
		case r.Method == http.MethodHead:
			if _, loaded := broken.LoadOrStore(r.URL.Path, true); !loaded {
				return nil, io.ErrUnexpectedEOF // one broken segment request per account
			}
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			body = "#EXTM3U\n#EXTINF:2,\n" + strings.TrimSuffix(r.URL.Path, ".m3u8") + ".ts\n"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			login := fmt.Sprintf("user%d", i)
			c := NewGQLClient(h, config.Default(), &parser.Account{UserID: fmt.Sprint(i)})
			variant := "https://test.ttvnw.net/" + login + ".m3u8"
			for heartbeat := 0; heartbeat < 3; heartbeat++ {
				result, err := c.WatchHeartbeat(ctx, variant, Stream{Login: login, ChannelID: "1", BroadcastID: "2", GameID: "3"})
				if err != nil {
					t.Error(err)
					return
				}
				if result.Method != "HLS-сегменты и minute-watched beacon" {
					t.Errorf("client %d did not recover full heartbeat: %s", i, result.Method)
					return
				}
				variant = result.VariantURL
			}
		}(i)
	}
	wg.Wait()
}

type discardWSConn struct{}

func (discardWSConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (discardWSConn) Write(p []byte) (int, error)      { return len(p), nil }
func (discardWSConn) Close() error                     { return nil }
func (discardWSConn) LocalAddr() net.Addr              { return nil }
func (discardWSConn) RemoteAddr() net.Addr             { return nil }
func (discardWSConn) SetDeadline(time.Time) error      { return nil }
func (discardWSConn) SetReadDeadline(time.Time) error  { return nil }
func (discardWSConn) SetWriteDeadline(time.Time) error { return nil }

func TestReviewWebSocketFragmentedLimit(t *testing.T) {
	var input bytes.Buffer
	for i := 0; i < 2; i++ {
		opcode := byte(0x01)
		if i == 1 {
			opcode = 0x80 // final continuation
		}
		input.Write([]byte{opcode, 127})
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], 600000)
		input.Write(length[:])
		input.Write(make([]byte, 600000))
	}
	c := &wsConn{conn: discardWSConn{}, br: bufio.NewReader(&input)}
	if _, err := c.readMessage(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("fragmented message not bounded: %v", err)
	}
}

func FuzzWebSocketFrames(f *testing.F) {
	for _, seed := range [][]byte{{0x81, 0}, {0x89, 0}, {0x81, 127, 255, 255, 255, 255, 255, 255, 255, 255}, {0x01, 0, 0x80, 0}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		c := &wsConn{conn: discardWSConn{}, br: bufio.NewReader(bytes.NewReader(input))}
		_, _ = c.readMessage() // every malformed frame must return without panic
	})
}
