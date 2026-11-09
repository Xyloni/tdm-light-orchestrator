package parser

import (
	"fmt"
	"os"
	"path/filepath"
)

// MigrateLegacyFile preserves an existing installation's list when adopting
// account.txt. An existing target always takes precedence over the legacy file.
func MigrateLegacyFile(target string) error {
	if filepath.Base(target) != "account.txt" {
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	legacy := filepath.Join(filepath.Dir(target), "accounts.txt")
	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("%s → %s: %w", legacy, target, err)
	}
	return nil
}
