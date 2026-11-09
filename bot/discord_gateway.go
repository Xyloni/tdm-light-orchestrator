package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type gatewayResume struct {
	session, url string
	sequence     int64
	known        bool
}

type gatewayConfigurationError struct{ code int }

func (e *gatewayConfigurationError) Error() string {
	return fmt.Sprintf("gateway configuration: %d", e.code)
}

type gatewayFrame struct {
	Op       int             `json:"op"`
	Sequence *int64          `json:"s"`
	Type     string          `json:"t"`
	Data     json.RawMessage `json:"d"`
}

func (d *Discord) gateway(ctx context.Context) {
	state := gatewayResume{}
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := d.connectGateway(ctx, &state)
		if ctx.Err() != nil {
			return
		}
		var configuration *gatewayConfigurationError
		if errors.As(err, &configuration) {
			log.Printf("[discord] Discord отклонил настройки (код %d); проверьте токен и настройки приложения, затем перезапустите", configuration.code)
			<-ctx.Done()
			return
		}
		if time.Since(started) > 2*time.Minute {
			backoff = 5 * time.Second
		}
		if err != nil {
			log.Printf("[discord] соединение прервано; повтор через %s", backoff)
		}
		if !sleepCtx(ctx, backoff+time.Duration(rand.Intn(1000))*time.Millisecond) {
			return
		}
		backoff *= 2
		if backoff > time.Minute {
			backoff = time.Minute
		}
	}
}

func (d *Discord) connectGateway(ctx context.Context, state *gatewayResume) error {
	endpoint := d.gatewayURL
	if state.known && state.url != "" {
		endpoint = state.url
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.DialContext(ctx, endpoint, nil)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("gateway: connect failed")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var hello gatewayFrame
	if conn.ReadJSON(&hello) != nil || hello.Op != 10 {
		return fmt.Errorf("gateway: invalid hello")
	}
	var settings struct {
		Interval float64 `json:"heartbeat_interval"`
	}
	if json.Unmarshal(hello.Data, &settings) != nil || settings.Interval < 1000 || settings.Interval > 120000 {
		return fmt.Errorf("gateway: invalid heartbeat interval")
	}
	interval := time.Duration(settings.Interval * float64(time.Millisecond))
	var writes sync.Mutex
	write := func(op int, data any) error {
		writes.Lock()
		defer writes.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(map[string]any{"op": op, "d": data})
	}
	var sequence atomic.Int64
	sequence.Store(state.sequence)
	var sequenceKnown atomic.Bool
	sequenceKnown.Store(state.known)
	var pendingAck atomic.Bool
	heartbeat := func() error {
		var seq any
		if sequenceKnown.Load() {
			seq = sequence.Load()
		}
		pendingAck.Store(true)
		return write(1, seq)
	}
	if state.known && state.session != "" {
		err = write(6, map[string]any{"token": d.api.token, "session_id": state.session, "seq": state.sequence})
	} else {
		err = write(2, map[string]any{"token": d.api.token, "intents": 0, "properties": map[string]string{"os": runtime.GOOS, "browser": "tdm-light", "device": "tdm-light"}})
	}
	if err != nil {
		return fmt.Errorf("gateway: identify failed")
	}
	beatCtx, cancel := context.WithCancel(ctx)
	beatDone := make(chan struct{})
	go func() {
		defer close(beatDone)
		if !sleepCtx(beatCtx, time.Duration(rand.Int63n(int64(interval)))) {
			return
		}
		for beatCtx.Err() == nil {
			if pendingAck.Load() || heartbeat() != nil {
				conn.Close()
				return
			}
			if !sleepCtx(beatCtx, interval) {
				return
			}
		}
	}()
	defer func() { cancel(); conn.Close(); <-beatDone }()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ready := false
	for ctx.Err() == nil {
		var frame gatewayFrame
		if err := conn.ReadJSON(&frame); err != nil {
			if closeErr, ok := err.(*websocket.CloseError); ok {
				switch closeErr.Code {
				case 4007, 4009:
					*state = gatewayResume{}
				case 4004, 4010, 4011, 4012, 4013, 4014:
					return &gatewayConfigurationError{code: closeErr.Code}
				}
			}
			return fmt.Errorf("gateway: read failed")
		}
		switch frame.Op {
		case 0:
			switch frame.Type {
			case "READY":
				var data struct {
					Session     string `json:"session_id"`
					URL         string `json:"resume_gateway_url"`
					Application struct {
						ID string `json:"id"`
					} `json:"application"`
				}
				if json.Unmarshal(frame.Data, &data) != nil || data.Session == "" {
					return fmt.Errorf("gateway: invalid ready")
				}
				state.session = data.Session
				// Never send the bot token to an arbitrary resume URL.
				state.url = validatedResumeURL(data.URL)
				if data.Application.ID != "" {
					select {
					case d.registration <- data.Application.ID:
					default:
					}
				}
				ready = true
				log.Println("[discord] подключён")
			case "RESUMED":
				ready = true
			case "INTERACTION_CREATE":
				var interaction discordInteraction
				if json.Unmarshal(frame.Data, &interaction) == nil {
					d.enqueue(interaction)
				}
			}
			if frame.Sequence != nil {
				state.sequence = *frame.Sequence
				state.known = true
				sequence.Store(*frame.Sequence)
				sequenceKnown.Store(true)
			}
		case 1:
			if heartbeat() != nil {
				return fmt.Errorf("gateway: heartbeat failed")
			}
		case 7:
			return fmt.Errorf("gateway: reconnect requested")
		case 9:
			var resumable bool
			_ = json.Unmarshal(frame.Data, &resumable)
			if !resumable {
				*state = gatewayResume{}
			}
			return fmt.Errorf("gateway: invalid session")
		case 11:
			pendingAck.Store(false)
		}
		if ready {
			_ = conn.SetReadDeadline(time.Now().Add(2*interval + 10*time.Second))
		}
	}
	return ctx.Err()
}

func validatedResumeURL(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "wss" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return ""
	}
	host := u.Hostname()
	if host != "gateway.discord.gg" && !(strings.HasPrefix(host, "gateway-") && strings.HasSuffix(host, ".discord.gg")) {
		return ""
	}
	if u.Path != "" && u.Path != "/" {
		return ""
	}
	u.RawQuery = "v=10&encoding=json"
	return u.String()
}
