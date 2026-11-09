package twitch

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

type diagnosticOperationKey struct{}

var diagnosticActive atomic.Int64

type segmentStats struct {
	count, reused, slow int64
	total, max          time.Duration
}

var segmentDiagnostics struct {
	sync.Mutex
	stats segmentStats
}

func recordSegmentSuccess(elapsed time.Duration, reused bool) {
	segmentDiagnostics.Lock()
	defer segmentDiagnostics.Unlock()
	stats := &segmentDiagnostics.stats
	stats.count++
	stats.total += elapsed
	if elapsed > stats.max {
		stats.max = elapsed
	}
	if elapsed >= time.Second {
		stats.slow++
	}
	if reused {
		stats.reused++
	}
}

// FlushSegmentDiagnostics logs one summary and starts a new collection interval.
func FlushSegmentDiagnostics() {
	segmentDiagnostics.Lock()
	stats := segmentDiagnostics.stats
	segmentDiagnostics.stats = segmentStats{}
	segmentDiagnostics.Unlock()
	if stats.count != 0 {
		log.Printf("diagnostic hls_segments success=%d reused=%d slow_ge_1s=%d avg_ms=%d max_ms=%d", stats.count, stats.reused, stats.slow, (stats.total / time.Duration(stats.count)).Milliseconds(), stats.max.Milliseconds())
	}
}

// Never log URLs, headers, bodies, IP addresses or raw transport errors.
type diagnosticTransport struct {
	base    http.RoundTripper
	account string
}

func (d *diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	started := time.Now()
	active := diagnosticActive.Add(1)
	operation, _ := r.Context().Value(diagnosticOperationKey{}).(string)
	if operation == "" {
		operation = "http"
	}
	var mu sync.Mutex
	stage := "connection"
	var dns, dial, tlsAt time.Time
	var dnsMS, dialMS, tlsMS, connMS, headersMS int64
	reused := false
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { mu.Lock(); dns = time.Now(); stage = "dns"; mu.Unlock() },
		DNSDone:           func(httptrace.DNSDoneInfo) { mu.Lock(); dnsMS = time.Since(dns).Milliseconds(); mu.Unlock() },
		ConnectStart:      func(string, string) { mu.Lock(); dial = time.Now(); stage = "dial"; mu.Unlock() },
		ConnectDone:       func(string, string, error) { mu.Lock(); dialMS = time.Since(dial).Milliseconds(); mu.Unlock() },
		TLSHandshakeStart: func() { mu.Lock(); tlsAt = time.Now(); stage = "tls"; mu.Unlock() },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { mu.Lock(); tlsMS = time.Since(tlsAt).Milliseconds(); mu.Unlock() },
		GotConn: func(i httptrace.GotConnInfo) {
			mu.Lock()
			reused = i.Reused
			connMS = time.Since(started).Milliseconds()
			stage = "write"
			mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { mu.Lock(); stage = "headers"; mu.Unlock() },
	}
	resp, err := d.base.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
	status := 0
	proto := ""
	if resp != nil {
		status = resp.StatusCode
		proto = resp.Proto
	}
	headersMS = time.Since(started).Milliseconds()
	finish := func(bodyErr error) {
		diagnosticActive.Add(-1)
		mu.Lock()
		defer mu.Unlock()
		if bodyErr == nil && r.Method == http.MethodHead && status == http.StatusOK {
			recordSegmentSuccess(time.Since(started), reused)
			return
		}
		detail := "ok"
		if bodyErr != nil {
			detail = "transport_error"
			if errors.Is(bodyErr, context.DeadlineExceeded) {
				detail = "timeout"
			} else if errors.Is(bodyErr, context.Canceled) {
				detail = "canceled"
			} else if e, ok := bodyErr.(net.Error); ok && e.Timeout() {
				detail = "network_timeout"
			}
		}
		log.Printf("[%s] diagnostic http operation=%s method=%s host=%s status=%d protocol=%s stage=%s elapsed_ms=%d conn_ms=%d headers_ms=%d dns_ms=%d dial_ms=%d tls_ms=%d reused=%t active_at_start=%d result=%s", d.account, operation, r.Method, r.URL.Hostname(), status, proto, stage, time.Since(started).Milliseconds(), connMS, headersMS, dnsMS, dialMS, tlsMS, reused, active, detail)
	}
	if err != nil {
		finish(err)
		return resp, err
	}
	mu.Lock()
	stage = "body"
	mu.Unlock()
	if resp.Body == nil {
		finish(nil)
	} else {
		resp.Body = &diagnosticBody{ReadCloser: resp.Body, finish: finish}
	}
	return resp, nil
}

type diagnosticBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(error)
}

func (b *diagnosticBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil {
		reported := e
		if e == io.EOF {
			reported = nil
		}
		b.once.Do(func() { b.finish(reported) })
	}
	return n, e
}
func (b *diagnosticBody) Close() error {
	e := b.ReadCloser.Close()
	b.once.Do(func() { b.finish(e) })
	return e
}
