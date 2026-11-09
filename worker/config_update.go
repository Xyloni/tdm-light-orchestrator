package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/xyloni/tdm-light-orchestrator/config"
)

func saveDropExclusion(filePath, name string, exclude bool) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return fmt.Errorf("название должно содержать от 1 до 200 символов")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("название содержит управляющие символы")
		}
	}
	if _, exists := os.LookupEnv("IGNORED_DROP_NAMES"); exists {
		return fmt.Errorf("IGNORED_DROP_NAMES задан в окружении; измените его там или удалите переменную")
	}
	// Read the latest file for every command; queued commands during reload must
	// not overwrite exclusions saved by the previous command.
	cfg, err := config.Load(filepath.Join(filepath.Dir(filePath), "config.json"), filePath)
	if err != nil {
		return err
	}
	names := []string{}
	found := false
	for _, previous := range cfg.IgnoredDropNames {
		if strings.EqualFold(strings.TrimSpace(previous), name) {
			found = true
			if !exclude {
				continue
			}
		}
		names = append(names, previous)
	}
	if exclude && !found {
		names = append(names, name)
	}
	data, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return updateEnvSetting(filePath, "IGNORED_DROP_NAMES", string(data))
}

// Environment configuration keeps its documented priority. Do not report a
// successful change when that environment would silently undo it on reload.
func saveTargetGame(filePath, game string) error {
	game = strings.TrimSpace(game)
	if configured, ok := os.LookupEnv("TARGET_GAME"); ok && configured != game {
		return fmt.Errorf("TARGET_GAME задан в окружении; измените его там или удалите переменную и перезапустите сервис")
	}
	return updateEnvSetting(filePath, "TARGET_GAME", game)
}

var envWriteMu sync.Mutex

func updateEnvSetting(filePath, key, value string) error {
	envWriteMu.Lock()
	defer envWriteMu.Unlock()
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("название игры пустое или содержит перевод строки")
	}

	data, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("не удалось прочитать config.env: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(filePath); statErr == nil {
		mode = info.Mode().Perm()
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	updated := false
	for i, line := range lines {
		bom := ""
		if strings.HasPrefix(line, "\uFEFF") {
			bom = "\uFEFF"
			line = strings.TrimPrefix(line, bom)
		}
		name, _, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == key {
			lines[i] = bom + key + "=" + value
			updated = true
		}
	}
	if !updated {
		lines = append(lines, key+"="+value)
	}
	updatedData := []byte(strings.Join(lines, "\n") + "\n")

	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".config-env-*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось создать временный config.env: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("не удалось установить права config.env: %w", err)
	}
	if _, err := tmp.Write(updatedData); err != nil {
		tmp.Close()
		return fmt.Errorf("не удалось записать config.env: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("не удалось сохранить config.env: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть config.env: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("не удалось обновить config.env: %w", err)
	}
	return nil
}
