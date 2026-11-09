package orchestrator

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const logDateLayout = "2006-01-02"

var errLogArchiveLimit = errors.New("log archive limit exceeded")

type logArchiveBuffer struct{ bytes.Buffer }

func (b *logArchiveBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, errLogArchiveLimit
	}
	return b.Buffer.Write(p)
}

func (s *scheduler) exportLogs(ctx context.Context, date string) (name string, data []byte, message string) {
	now := time.Now()
	if date == "" {
		date = now.Format(logDateLayout)
	}
	for days := 0; days < 3; days++ {
		if date == now.AddDate(0, 0, -days).Format(logDateLayout) {
			return archiveDailyLogs(ctx, filepath.Join(filepath.Dir(s.path), "logs"), date)
		}
	}
	return "", nil, "Укажите дату в формате ГГГГ-ММ-ДД за сегодня или два предыдущих дня по времени сервера."
}

func archiveDailyLogs(ctx context.Context, root, date string) (string, []byte, string) {
	var output logArchiveBuffer
	archive := zip.NewWriter(&output)
	suffix := "-" + date + ".log"
	remaining := int64(64 << 20)
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // A file may be rotated or pruned during export.
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.TrimSuffix(entry.Name(), ".1"), suffix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > remaining || count >= 10000 {
			return errLogArchiveLimit
		}
		dst, err := archive.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if _, err := io.CopyN(dst, file, info.Size()); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		remaining -= info.Size()
		count++
		return nil
	})
	if err == nil {
		err = archive.Close()
	}
	switch {
	case errors.Is(err, errLogArchiveLimit):
		return "", nil, "Логи за этот день превышают лимит выгрузки (8 МиБ ZIP или 64 МиБ исходных файлов)."
	case err != nil:
		return "", nil, "Не удалось собрать архив логов. Попробуйте ещё раз."
	case count == 0:
		return "", nil, "За выбранный день логов пока нет."
	}
	return "tdm-logs-" + date + ".zip", output.Bytes(), ""
}
