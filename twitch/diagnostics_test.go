package twitch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/xyloni/tdm-light-orchestrator/config"
	"github.com/xyloni/tdm-light-orchestrator/parser"
)

func TestDiagnosticsRedactsTransportSecretsAndClosesCounter(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	cfg := config.Default()
	cfg.Diagnostics = true
	h := &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: context.DeadlineExceeded}
	})}
	c := NewGQLClient(h, cfg, &parser.Account{Login: "fixture"})
	_, err := c.GetMasterPlaylist(context.Background(), "https://usher.ttvnw.net/api/channel/hls/secret-token.m3u8?sig=private-signature")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("error cause lost")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "private-signature") {
		t.Fatal("master error leaks signed URL")
	}
	text := output.String()
	if strings.Contains(text, "secret-token") || strings.Contains(text, "private-signature") || !strings.Contains(text, "diagnostic http") || !strings.Contains(text, "result=timeout") {
		t.Fatal("unsafe or missing diagnostic", text)
	}
	if diagnosticActive.Load() != 0 {
		t.Fatal("active requests leaked")
	}
}

func TestDiagnosticsSummarizesSuccessfulSegmentsAndKeepsErrors(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	status := http.StatusOK
	d := &diagnosticTransport{account: "fixture", base: reviewTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodHead, "https://test.ttvnw.net/segment.ts", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if i == 3 {
			status = http.StatusNotFound
		}
		resp, err := d.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	FlushSegmentDiagnostics()
	FlushSegmentDiagnostics()
	got := output.String()
	if strings.Count(got, "diagnostic http") != 1 || !strings.Contains(got, "status=404") ||
		strings.Count(got, "diagnostic hls_segments success=3") != 1 {
		t.Fatal(got)
	}
}

func TestHLSChecksOnlyNewSegmentsAndDoesNotCacheFailedOnes(t *testing.T) {
	playlist := "#EXTM3U\na.ts\nb.ts\n"
	calls := map[string]int{}
	fail := false
	h := &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		body := playlist
		status := 200
		if r.Method == "HEAD" {
			calls[r.URL.Path]++
			body = ""
			if fail && strings.HasSuffix(r.URL.Path, "c.ts") {
				status = 503
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	c := NewGQLClient(h, config.Default(), &parser.Account{})
	check := func() {
		t.Helper()
		if err := c.watchPlaylistSegments(context.Background(), "https://test.ttvnw.net/live.m3u8"); err != nil {
			t.Fatal(err)
		}
	}
	check()
	check()
	if calls["/a.ts"] != 1 || calls["/b.ts"] != 1 {
		t.Fatal("segments checked twice", calls)
	}
	playlist = "#EXTM3U\nb.ts\nc.ts\n"
	fail = true
	if err := c.watchPlaylistSegments(context.Background(), "https://test.ttvnw.net/live.m3u8"); err == nil {
		t.Fatal("failed segment accepted")
	}
	fail = false
	check()
	if calls["/c.ts"] != 2 || calls["/b.ts"] != 1 {
		t.Fatal("new/failed segment cache incorrect", calls)
	}
	if len(c.segments) != 2 {
		t.Fatal("cache not limited to current window")
	}
}
