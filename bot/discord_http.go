package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Only five fixed route keys are used; interaction tokens never become map keys.
// Rate-limit waits and retries are cancellable, including during hot reload.
type discordAPI struct {
	client      *http.Client
	base, token string
	mu          sync.Mutex
	until       map[string]time.Time
	global      time.Time
}

func (a *discordAPI) wait(ctx context.Context, route string) error {
	for {
		a.mu.Lock()
		next := a.until[route]
		if a.global.After(next) {
			next = a.global
		}
		a.mu.Unlock()
		if delay := time.Until(next); delay > 0 {
			if !sleepCtx(ctx, delay) {
				return ctx.Err()
			}
		} else {
			return ctx.Err()
		}
	}
}

func (a *discordAPI) deferRoute(route string, delay time.Duration, global bool) {
	if delay < 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.until == nil {
		a.until = map[string]time.Time{}
	}
	next := time.Now().Add(delay)
	if next.After(a.until[route]) {
		a.until[route] = next
	}
	if global && next.After(a.global) {
		a.global = next
	}
}

func (a *discordAPI) request(ctx context.Context, method, path, route string, payload, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: JSON encoding error")
	}
	return a.requestBody(ctx, method, path, route, "application/json", data, result)
}

func (a *discordAPI) requestBody(ctx context.Context, method, path, route, contentType string, data []byte, result any) error {
	client := a.client
	if contentType != "application/json" {
		noRedirects := *client
		noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &noRedirects
	}
	for attempt := 0; attempt < 4; attempt++ {
		if err := a.wait(ctx, route); err != nil {
			return err
		}
		req, e := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(data))
		if e != nil {
			return fmt.Errorf("discord: invalid request")
		}
		req.Header.Set("Authorization", "Bot "+a.token)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("User-Agent", "TDM-Light (https://github.com/Xyloni/tdm-light-orchestrator)")
		resp, e := client.Do(req)
		if e != nil {
			return fmt.Errorf("discord: connection failed")
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if e != nil {
			return fmt.Errorf("discord: response read failed")
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if seconds, e := strconv.ParseFloat(resp.Header.Get("X-RateLimit-Reset-After"), 64); e == nil && seconds >= 0 && seconds <= 86400 {
				a.deferRoute(route, time.Duration(seconds*float64(time.Second)), false)
			}
		}
		if resp.StatusCode == 429 {
			var rate struct {
				RetryAfter float64 `json:"retry_after"`
				Global     bool    `json:"global"`
			}
			if json.Unmarshal(body, &rate) != nil || rate.RetryAfter <= 0 || rate.RetryAfter > 86400 {
				return fmt.Errorf("discord: invalid rate limit response")
			}
			a.deferRoute(route, time.Duration(rate.RetryAfter*float64(time.Second))+100*time.Millisecond, rate.Global)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("discord: HTTP %d", resp.StatusCode)
		}
		if result != nil {
			if json.Unmarshal(body, result) != nil {
				return fmt.Errorf("discord: invalid JSON response")
			}
		}
		return nil
	}
	return fmt.Errorf("discord: rate limit retry budget exhausted")
}
