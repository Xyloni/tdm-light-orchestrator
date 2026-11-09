package twitch

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Минимальный WebSocket-клиент (RFC 6455, только текстовые фреймы) и
// обёртка Twitch PubSub для событий дропов. Опционально (use_pubsub),
// по умолчанию выключено: heartbeat-фарм работает чисто на HTTP.

// --- WebSocket ---

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

func wsDial(ctx context.Context, host, path string) (*wsConn, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}}
	raw, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	// Cancellation and a deadline also cover the HTTP upgrade, not just TLS dial.
	stopClose := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopClose()
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	if _, err := fmt.Fprintf(raw,
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, host, key); err != nil {
		raw.Close()
		return nil, err
	}
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		raw.Close()
		return nil, err
	}
	accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != http.StatusSwitchingProtocols ||
		!strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") ||
		resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(accept[:]) {
		raw.Close()
		return nil, fmt.Errorf("WS handshake: invalid upgrade (HTTP %d)", resp.StatusCode)
	}
	_ = raw.SetDeadline(time.Time{})
	return &wsConn{conn: raw, br: br}, nil
}

// writeFrame отправляет маскированный клиентский фрейм.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	header := []byte{0x80 | opcode}
	n := len(payload)
	var ext []byte
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 65535:
		header = append(header, 0x80|126)
		ext = make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(n))
	default:
		header = append(header, 0x80|127)
		ext = make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(n))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	buf := make([]byte, 0, len(header)+len(ext)+4+n)
	buf = append(buf, header...)
	buf = append(buf, ext...)
	buf = append(buf, mask...)
	buf = append(buf, masked...)

	_ = c.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

func (c *wsConn) WriteText(payload []byte) error { return c.writeFrame(0x1, payload) }

// readMessage читает одно (возможно, фрагментированное) сообщение,
// автоматически отвечая pong на ping.
func (c *wsConn) readMessage() ([]byte, error) {
	const maxMessageBytes = 1 << 20
	var msg []byte
	started := false
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(c.br, h); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		opcode := h[0] & 0x0F
		masked := h[1]&0x80 != 0
		if h[0]&0x70 != 0 || masked {
			return nil, fmt.Errorf("invalid server WS frame flags")
		}
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > maxMessageBytes {
			return nil, fmt.Errorf("слишком большой WS-фрейм: %d байт", n)
		}
		if opcode >= 8 && (!fin || n > 125) {
			return nil, fmt.Errorf("invalid WS control frame")
		}
		if opcode < 8 {
			if opcode == 1 && !started {
				started = true
			} else if opcode != 0 || !started {
				return nil, fmt.Errorf("invalid WS message fragmentation")
			}
			if n > uint64(maxMessageBytes-len(msg)) {
				return nil, fmt.Errorf("WS message exceeds %d bytes", maxMessageBytes)
			}
		} else if opcode != 8 && opcode != 9 && opcode != 10 {
			return nil, fmt.Errorf("unsupported WS opcode")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8:
			return nil, fmt.Errorf("WS закрыт сервером")
		case 0x9: // ping → pong
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA: // pong
			continue
		}
		msg = append(msg, payload...)
		if fin {
			return msg, nil
		}
	}
}

// --- Twitch PubSub ---

// PubSubLoop слушает события дропов аккаунта через wss://pubsub-edge.twitch.tv.
// При обрыве переподключается с экспоненциальным backoff, пока жив ctx.
func (c *GQLClient) PubSubLoop(ctx context.Context, onEvent func(topic string, msg json.RawMessage)) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		_ = c.pubsubOnce(ctx, onEvent)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Minute {
			backoff *= 2
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
		}
	}
}

func (c *GQLClient) pubsubOnce(ctx context.Context, onEvent func(topic string, msg json.RawMessage)) error {
	ws, err := wsDial(ctx, "pubsub-edge.twitch.tv:443", "/")
	if err != nil {
		return err
	}
	defer ws.conn.Close()

	listen, _ := json.Marshal(map[string]any{
		"type":  "LISTEN",
		"nonce": "tdm-light-" + c.Account.UserID,
		"data": map[string]any{
			"topics":     []string{"user-drop-events." + c.Account.UserID},
			"auth_token": c.Account.OAuth,
		},
	})
	if err := ws.WriteText(listen); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	readDone := make(chan struct{})
	defer func() {
		ws.conn.Close()
		<-readDone
	}()
	go func() {
		defer close(readDone)
		for {
			_ = ws.conn.SetReadDeadline(time.Now().Add(6 * time.Minute))
			m, err := ws.readMessage()
			if err != nil {
				errCh <- err
				return
			}
			var env struct {
				Type string `json:"type"`
				Data struct {
					Topic   string          `json:"topic"`
					Message json.RawMessage `json:"Message"`
					Msg     json.RawMessage `json:"msg"`
				} `json:"data"`
			}
			if json.Unmarshal(m, &env) != nil {
				continue
			}
			switch env.Type {
			case "RECONNECT":
				errCh <- fmt.Errorf("pubsub: сервер запросил reconnect")
				return
			case "MESSAGE":
				msg := env.Data.Message
				if len(msg) == 0 {
					msg = env.Data.Msg
				}
				// Внутреннее сообщение — JSON-строка внутри JSON.
				var s string
				if json.Unmarshal(msg, &s) == nil {
					msg = json.RawMessage(s)
				}
				if len(msg) > 0 && onEvent != nil {
					onEvent(env.Data.Topic, msg)
				}
			}
		}
	}()

	// Twitch требует PING не реже раза в 4 минуты.
	ping := time.NewTicker(3*time.Minute + 30*time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ping.C:
			if err := ws.WriteText([]byte(`{"type":"PING"}`)); err != nil {
				return err
			}
		}
	}
}
