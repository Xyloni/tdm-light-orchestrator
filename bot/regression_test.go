package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReviewRestartUpdateMustNotReplay(t *testing.T) {
	reloads := 0
	state := new(PollState)
	transport := testTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true}`
		if strings.HasSuffix(r.URL.Path, "/getUpdates") && r.URL.Query().Get("offset") == "0" {
			body = `{"ok":true,"result":[{"update_id":42,"message":{"from":{"id":1},"text":"/restart"}}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	for i := 0; i < 2; i++ {
		b := New("test", 1, nil, nil, func() { reloads++ }, nil, nil, nil, state)
		b.http.Transport = transport
		if err := b.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if reloads != 1 {
		t.Fatalf("same restart update processed %d times across manager recreation", reloads)
	}
}

func TestReviewCancellationPreservesUnprocessedUpdates(t *testing.T) {
	state := new(PollState)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := 0
	transport := testTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true}`
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if r.URL.Query().Get("offset") == "0" {
				body = `{"ok":true,"result":[{"update_id":42,"message":{"from":{"id":1},"text":"/restart"}},{"update_id":43,"message":{"from":{"id":1},"text":"/restart"}}]}`
			} else if r.URL.Query().Get("offset") == "43" {
				body = `{"ok":true,"result":[{"update_id":43,"message":{"from":{"id":1},"text":"/restart"}}]}`
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	b := New("test", 1, nil, nil, func() { reloads++; cancel() }, nil, nil, nil, state)
	b.http.Transport = transport
	_ = b.poll(ctx)
	if state.offset.Load() != 43 || reloads != 1 {
		t.Fatal("cancelled polling consumed an unprocessed update")
	}
	b = New("test", 1, nil, nil, func() { reloads++ }, nil, nil, nil, state)
	b.http.Transport = transport
	if err := b.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.offset.Load() != 44 || reloads != 2 {
		t.Fatal("next bot lost the remaining update")
	}
}
