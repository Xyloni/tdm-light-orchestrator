package worker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUnifiedLogConcurrentAccountsAppendAndDrain(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenUnifiedLog(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 625; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); fmt.Fprintf(w, "[fixture-%d] progress\n", i) }(i)
	}
	wg.Wait()
	w.Close()
	w, err = OpenUnifiedLog(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(w, "restarted")
	w.Close()
	b, err := os.ReadFile(filepath.Join(dir, "tdm-light.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "progress") != 625 || !strings.HasSuffix(string(b), "restarted\n") {
		t.Fatal("missing records after drain/restart")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("expected a single file")
	}
}

type stalledConsole struct {
	entered, release chan struct{}
	once             sync.Once
}

func (s *stalledConsole) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return len(p), nil
}
func TestUnifiedLogSlowDestinationDoesNotBlockFarming(t *testing.T) {
	dir := t.TempDir()
	dest := &stalledConsole{entered: make(chan struct{}), release: make(chan struct{})}
	w, err := OpenUnifiedLog(dir, dest)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(w, "first")
	<-dest.entered
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			fmt.Fprintln(w, "burst")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer blocked on destination")
	}
	close(dest.release)
	w.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "tdm-light.log"))
	if !strings.Contains(string(b), "log_queue_dropped=") {
		t.Fatal("overflow must be visible")
	}
}
