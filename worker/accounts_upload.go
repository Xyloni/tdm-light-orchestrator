package worker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xyloni/tdm-light-orchestrator/parser"
)

// replaceAccountsFile validates an uploaded account list before replacing the
// configured file. The temporary file is created alongside the target so the
// final rename stays on the same filesystem.
func replaceAccountsFile(target string, content []byte) error {
	if len(content) == 0 {
		return fmt.Errorf("файл пуст")
	}
	if len(content) > 2<<20 {
		return fmt.Errorf("файл превышает лимит 2 МБ")
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".accounts-upload-*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("не удалось записать временный файл: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("не удалось сохранить временный файл: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть временный файл: %w", err)
	}
	if _, err := parser.ParseFile(tmpPath); err != nil {
		return fmt.Errorf("формат account.txt неверный (%w)", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("не удалось заменить %s: %w", target, err)
	}
	return nil
}
