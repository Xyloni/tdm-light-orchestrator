package worker

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xyloni/tdm-light-orchestrator/internal/dailylog"
)

const maxLogFileBytes = 256 << 10 // At most 256 KiB current + 256 KiB previous per account.

func (s *AccountState) appendFileLog(line string) error {
	return s.appendFileLogAt(time.Now(), line)
}

func (s *AccountState) appendFileLogAt(at time.Time, line string) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if len(line) > maxLogFileBytes {
		line = line[:maxLogFileBytes-1] + "\n"
	}
	path := s.LogFile
	if s.DailyLogs {
		path = dailylog.Path(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), ".log"), at)
	}
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info != nil && info.Size()+int64(len(line)) > maxLogFileBytes {
		// Remove the previous generation first for Windows rename semantics.
		if err := os.Remove(path + ".1"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
