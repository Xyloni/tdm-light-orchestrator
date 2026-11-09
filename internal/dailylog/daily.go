package dailylog

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

func Path(dir, name string, at time.Time) string {
	return filepath.Join(dir, name+"-"+at.Format(dateLayout)+".log")
}

// Writer is owned by one writer or serialized by log.Logger.
type Writer struct {
	Dir, Name string
}

func (w *Writer) Write(p []byte) (int, error) {
	if err := os.MkdirAll(w.Dir, 0700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(Path(w.Dir, w.Name, time.Now()), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	n, err := f.Write(p)
	closeErr := f.Close()
	if err != nil {
		return n, err
	}
	return n, closeErr
}

func (w *Writer) Close() error {
	return nil
}

var _ io.WriteCloser = (*Writer)(nil)

// Prune keeps today's files and the preceding two calendar days. Undated
// legacy logs use their modification time. Other files are left alone.
func Prune(root string, now time.Time) error {
	cutoff := now.AddDate(0, 0, -2).Format(dateLayout)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := strings.TrimSuffix(d.Name(), ".1")
		if !strings.HasSuffix(name, ".log") {
			return nil
		}
		base := strings.TrimSuffix(name, ".log")
		if len(base) > len(dateLayout) {
			suffix := base[len(base)-len(dateLayout):]
			if _, err := time.Parse(dateLayout, suffix); err == nil {
				if suffix < cutoff {
					return os.Remove(path)
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(now.Add(-72 * time.Hour)) {
			return os.Remove(path)
		}
		return nil
	})
}
