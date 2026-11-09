package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func testDiscord() *Discord {
	return NewDiscord("secret-never-log", "123", Actions{Status: func() string { return "status" }, Logs: func(s string) string { return "logs " + s }, Game: func() string { return "game" }, SetGame: func(string) error { return nil }, Upload: func([]byte) error { return nil }})
}

func interaction(name string) discordInteraction {
	var i discordInteraction
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"id":"101001001001001001","application_id":"101002002002002002","token":"interaction-secret","type":2,"channel_id":"456","context":1,"user":{"id":"123"},"data":{"name":%q}}`, name)), &i)
	return i
}

func TestDiscordAuthorizationAndDuplicateDelivery(t *testing.T) {
	d := testDiscord()
	i := interaction("restart")
	d.enqueue(i)
	d.enqueue(i)
	if len(d.acks) != 1 {
		t.Fatal("duplicate command admitted")
	}
	i.ID = "2"
	i.User.ID = "999"
	d.enqueue(i)
	i.User.ID = "123"
	i.GuildID = "999"
	d.enqueue(i)
	if len(d.acks) != 1 {
		t.Fatal("unauthorized command admitted")
	}
	for n := 0; n < 10000; n++ {
		i := interaction("status")
		i.ID = fmt.Sprint(n + 1000)
		d.enqueue(i)
	}
	if len(d.acks) != cap(d.acks) {
		t.Fatal("queue bound not enforced")
	}
}

func TestDiscordRateLimitCancellation(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/users/@me/channels" {
					fmt.Fprint(w, `{"id":"456","type":1,"recipients":[{"id":"123"}]}`)
					return
				}
				calls.Add(1)
				if status == 429 {
					w.WriteHeader(429)
					fmt.Fprint(w, `{"retry_after":3600,"global":true}`)
				} else {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset-After", "3600")
					fmt.Fprint(w, `{}`)
				}
			}))
			defer server.Close()
			d := testDiscord()
			d.api.base = server.URL
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := d.Send(ctx, "hello")
			if status == 200 {
				if err != nil {
					t.Fatal(err)
				}
				err = d.Send(ctx, "again")
			}
			if err == nil || time.Since(start) > time.Second || calls.Load() != 1 {
				t.Fatalf("uncancellable limit: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestDiscordUploadBoundsAndNoCredentialsToCDN(t *testing.T) {
	d := testDiscord()
	d.api.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("bot token leaked to CDN")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("one\n"))}, nil
	})
	a := discordAttachment{Filename: "any.TXT", URL: "https://cdn.discordapp.com/attachments/1/2/file.txt"}
	if data, err := d.download(context.Background(), a); err != nil || string(data) != "one\n" {
		t.Fatalf("valid upload: %q %v", data, err)
	}
	for _, link := range []string{"http://cdn.discordapp.com/attachments/x", "https://evil.example/attachments/x", "https://cdn.discordapp.com.evil.example/attachments/x", "https://cdn.discordapp.com:443/attachments/x", "https://user@cdn.discordapp.com/attachments/x", "https://cdn.discordapp.com/no-attachment"} {
		a.URL = link
		if _, err := d.download(context.Background(), a); err == nil {
			t.Fatalf("accepted %s", link)
		}
	}
	a.URL = "https://cdn.discordapp.com/attachments/1/2/file.txt"
	d.api.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxAccountsUploadBytes+1)))}, nil
	})
	if _, err := d.download(context.Background(), a); err == nil {
		t.Fatal("oversized download accepted")
	}
}

func TestDiscordAccountsSlashUploadEphemeralAttachment(t *testing.T) {
	for _, host := range []string{"cdn.discordapp.com", "media.discordapp.net"} {
		for _, prefix := range []string{"attachments", "ephemeral-attachments"} {
			t.Run(host+"/"+prefix, func(t *testing.T) {
				d := testDiscord()
				content := "email:login:password:token:123:device\n"
				uploads := 0
				d.actions.Upload = func(data []byte) error {
					uploads++
					if string(data) != content {
						t.Fatal("upload content changed")
					}
					return nil
				}
				d.api.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != host || !strings.HasPrefix(r.URL.Path, "/"+prefix+"/") {
						t.Fatal("wrong attachment URL")
					}
					if r.URL.RawQuery != "ex=123&is=456&hm=signature" {
						t.Fatal("signed query changed")
					}
					if r.Header.Get("Authorization") != "" {
						t.Fatal("token sent to CDN")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(content))}, nil
				})
				i := interaction("accounts")
				payload := fmt.Sprintf(`{"name":"accounts","options":[{"name":"file","value":"789"}],"resolved":{"attachments":{"789":{"filename":"Новый текстовый документ (2).txt","size":64,"url":"https://%s/%s/123/789/file.txt?ex=123&is=456&hm=signature"}}}}`, host, prefix)
				if err := json.Unmarshal([]byte(payload), &i.Data); err != nil {
					t.Fatal(err)
				}
				text, reload := d.command(context.Background(), i)
				if uploads != 1 || !reload || !strings.Contains(text, "account.txt") {
					t.Fatalf("upload rejected: uploads=%d reload=%v response=%s", uploads, reload, text)
				}
			})
		}
	}
}

func TestDiscordCommandsAndRegistration(t *testing.T) {
	d := testDiscord()
	for _, name := range []string{"status", "help", "logs", "game", "restart"} {
		text, reload := d.command(context.Background(), interaction(name))
		if text == "" || reload != (name == "restart") {
			t.Fatalf("%s %s %v", name, text, reload)
		}
	}
	if len(discordCommands()) != 9 {
		t.Fatal("missing command")
	}
	for _, command := range discordCommands() {
		contexts, ok := command["contexts"].([]int)
		if !ok || len(contexts) != 1 || contexts[0] != 1 {
			t.Fatal("command exposed outside bot DM", command["name"])
		}
	}
	d.actions.SetGame = func(string) error { return fmt.Errorf("read only") }
	i := interaction("game")
	_ = json.Unmarshal([]byte(`{"name":"game","options":[{"name":"name","value":"New game"}]}`), &i.Data)
	if _, reload := d.command(context.Background(), i); reload {
		t.Fatal("failed save requested reload")
	}
}

func TestDiscordOnlyOwnerDirectMessages(t *testing.T) {
	d := testDiscord()
	i := interaction("status")
	if !d.accept(i) {
		t.Fatal("owner DM rejected")
	}
	for _, mutate := range []func(*discordInteraction){
		func(i *discordInteraction) { i.User.ID = "999" },
		func(i *discordInteraction) { i.GuildID = "999" },
		func(i *discordInteraction) { i.Member.User.ID = "123" },
		func(i *discordInteraction) { context := 2; i.Context = &context },
		func(i *discordInteraction) { context := 0; i.Context = &context },
		func(i *discordInteraction) { i.ChannelID = "" },
	} {
		i := interaction("restart")
		mutate(&i)
		if d.accept(i) {
			t.Fatal("non-owner or non-DM command accepted", i)
		}
	}
}

func TestDiscordOwnerDMResolvedOnce(t *testing.T) {
	d := testDiscord()
	var creates, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/@me/channels":
			creates.Add(1)
			var payload struct {
				Recipient string `json:"recipient_id"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Recipient != "123" {
				t.Error("wrong DM recipient")
			}
			fmt.Fprint(w, `{"id":"456","type":1,"recipients":[{"id":"123"}]}`)
		case "/channels/456/messages":
			sends.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	d.api.base = server.URL
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Send(context.Background(), "ready"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if creates.Load() != 1 || sends.Load() != 20 {
		t.Fatalf("creates=%d sends=%d", creates.Load(), sends.Load())
	}
}

func TestDiscordFailedOrWrongDMNeverReceivesNotification(t *testing.T) {
	for _, response := range []string{`{"id":"456","type":0,"recipients":[{"id":"123"}]}`, `{"id":"456","type":1,"recipients":[{"id":"999"}]}`, `{}`} {
		d := testDiscord()
		var creates atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/users/@me/channels" {
				t.Error("notification sent to unverified channel")
				return
			}
			creates.Add(1)
			fmt.Fprint(w, response)
		}))
		d.api.base = server.URL
		for n := 0; n < 2; n++ {
			if err := d.Send(context.Background(), "private"); err == nil {
				t.Error("invalid DM accepted")
			}
		}
		server.Close()
		if creates.Load() != 2 {
			t.Fatal("failed DM result was cached")
		}
	}
}

func TestDiscordDMResolutionAndGateCancel(t *testing.T) {
	d := testDiscord()
	started := make(chan struct{})
	d.api.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.Send(ctx, "first"); close(done) }()
	<-started
	waitCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := d.Send(waitCtx, "second"); err == nil {
		t.Fatal("DM gate ignored cancellation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DM resolution ignored cancellation")
	}
}

func TestDiscordAcknowledgementBeforeAction(t *testing.T) {
	for _, ackStatus := range []int{204, 500} {
		t.Run(fmt.Sprint(ackStatus), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var ack atomic.Bool
			var actions atomic.Int32
			finished := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/callback") {
					ack.Store(true)
					w.WriteHeader(ackStatus)
					if ackStatus == 500 {
						cancel()
					}
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			d := testDiscord()
			d.api.base = server.URL
			d.actions.Status = func() string {
				if !ack.Load() {
					t.Error("action preceded acknowledgement")
				}
				actions.Add(1)
				cancel()
				return "ok"
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); d.acknowledge(ctx) }()
			go func() { defer wg.Done(); d.execute(ctx) }()
			d.enqueue(interaction("status"))
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("command shutdown stuck")
			}
			want := int32(0)
			if ackStatus == 204 {
				want = 1
			}
			if actions.Load() != want {
				t.Fatal("unexpected action count")
			}
		})
	}
}

func TestGatewayResumeAndCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var auth struct {
			Op   int            `json:"op"`
			Data map[string]any `json:"d"`
		}
		if conn.ReadJSON(&auth) != nil {
			return
		}
		if connections.Add(1) == 1 {
			if auth.Op != 2 || auth.Data["intents"] != float64(0) {
				t.Error("invalid identify")
			}
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": 42, "t": "READY", "d": map[string]any{"session_id": "session", "application": map[string]string{"id": "101002002002002002"}}})
			_ = conn.WriteJSON(map[string]any{"op": 7, "d": nil})
			return
		}
		if auth.Op != 6 || auth.Data["seq"] != float64(42) || auth.Data["session_id"] != "session" {
			t.Error("resume lost state")
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 43, "t": "RESUMED", "d": map[string]any{}})
		close(ready)
		for {
			var f gatewayFrame
			if conn.ReadJSON(&f) != nil {
				return
			}
			if f.Op == 1 {
				_ = conn.WriteJSON(map[string]any{"op": 11, "d": nil})
			}
		}
	}))
	defer server.Close()
	d := testDiscord()
	d.gatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	state := gatewayResume{}
	if err := d.connectGateway(context.Background(), &state); err == nil {
		t.Fatal("expected reconnect")
	}
	if state.sequence != 42 || !state.known {
		t.Fatal("sequence lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.connectGateway(ctx, &state); close(done) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("no resume")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway cancellation stuck")
	}
}

func TestGatewayCancellationDuringHello(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		close(accepted)
		<-release
	}))
	defer server.Close()
	defer close(release)
	d := testDiscord()
	d.gatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = d.connectGateway(ctx, &gatewayResume{}); close(done) }()
	<-accepted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hello read ignored cancellation")
	}
}

func TestUnicodeMessageBounds(t *testing.T) {
	for _, s := range []string{strings.Repeat("🎁", 3000), strings.Repeat("я", 3000), "a\xffz"} {
		out := truncate(s, 1900)
		if !utf8.ValidString(out) || textUnits(out) > 1900 {
			t.Fatal("invalid message truncation")
		}
	}
}

func TestGatewayMissingHeartbeatAck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var auth any
		if c.ReadJSON(&auth) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "session"}})
		for {
			if c.ReadJSON(&auth) != nil {
				return
			}
		}
	}))
	defer server.Close()
	d := testDiscord()
	d.gatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	err := d.connectGateway(ctx, &gatewayResume{})
	if err == nil || ctx.Err() != nil || time.Since(start) > 3*time.Second {
		t.Fatal("missing ACK did not close the connection", err)
	}
}

func TestGatewayInvalidSessionClearsResume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}})
		var auth any
		if c.ReadJSON(&auth) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"op": 9, "d": false})
	}))
	defer server.Close()
	d := testDiscord()
	d.gatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	state := gatewayResume{session: "old", sequence: 42, known: true}
	_ = d.connectGateway(context.Background(), &state)
	if state.known || state.session != "" {
		t.Fatal("invalid session retained stale resume state")
	}
}

func TestResumeURLAllowlist(t *testing.T) {
	for _, u := range []string{"wss://gateway.discord.gg", "wss://gateway-us-east1-b.discord.gg"} {
		if validatedResumeURL(u) == "" {
			t.Fatal("valid Discord resume host rejected")
		}
	}
	for _, u := range []string{"ws://gateway.discord.gg", "wss://gateway.discord.gg.evil.example", "wss://evil.example", "wss://token@gateway.discord.gg", "wss://gateway.discord.gg:444", "wss://gateway.discord.gg/other"} {
		if validatedResumeURL(u) != "" {
			t.Fatal("unsafe resume URL accepted", u)
		}
	}
}

func TestCancelledCommandDoesNotChangeConfiguration(t *testing.T) {
	d := testDiscord()
	d.actions.SetGame = func(string) error { t.Fatal("cancelled command mutated configuration"); return nil }
	i := interaction("game")
	_ = json.Unmarshal([]byte(`{"name":"game","options":[{"name":"name","value":"Other game"}]}`), &i.Data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, reload := d.command(ctx, i); reload {
		t.Fatal("cancelled command requested reload")
	}
}
