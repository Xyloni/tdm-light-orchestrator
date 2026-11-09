package worker

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/internal/dailylog"
)

// The producer never waits for disk or console I/O. Close drains the queue.
type unifiedLog struct {
	q       chan []byte
	done    chan struct{}
	dropped atomic.Uint64
	once    sync.Once
}

func OpenUnifiedLog(dir string, console io.Writer) (io.WriteCloser, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "tdm-light.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return openUnifiedLog(f, console), nil
}

func OpenDailyUnifiedLog(dir string, console io.Writer) io.WriteCloser {
	return openUnifiedLog(&dailylog.Writer{Dir: dir, Name: "tdm-light"}, console)
}

func openUnifiedLog(f io.WriteCloser, console io.Writer) io.WriteCloser {
	w := &unifiedLog{q: make(chan []byte, 4096), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer f.Close()
		buf := bufio.NewWriterSize(f, 64<<10)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		report := func() {
			if n := w.dropped.Swap(0); n > 0 {
				fmt.Fprintf(buf, "%s diagnostic log_queue_dropped=%d\n", time.Now().Format("2006-01-02 15:04:05"), n)
			}
		}
		flush := func() {
			report()
			if err := buf.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "tdm-light log write failed: %v\n", err)
			}
		}
		for {
			select {
			case p, ok := <-w.q:
				if !ok {
					flush()
					return
				}
				if _, err := buf.Write(p); err != nil {
					fmt.Fprintf(os.Stderr, "tdm-light log write failed: %v\n", err)
				}
				if console != nil {
					_, _ = console.Write(p)
				}
			case <-tick.C:
				flush()
			}
		}
	}()
	return w
}
func (w *unifiedLog) Write(p []byte) (int, error) {
	copyP := append([]byte(nil), p...)
	select {
	case w.q <- copyP:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}
func (w *unifiedLog) Close() error { w.once.Do(func() { close(w.q); <-w.done }); return nil }
