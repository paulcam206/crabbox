//go:build !windows

package cli

import (
	"os"
	"path/filepath"
)

// userConfigDirectory resolves the per-user configuration root.
//
// os.UserConfigDir already honors XDG_CONFIG_HOME on Unix, but not on Darwin,
// where it returns ~/Library/Application Support. Crabbox supports both roots,
// so the override is applied explicitly here to keep macOS consistent with the
// documented XDG behavior.
func userConfigDirectory() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Clean(dir), nil
	}
	return os.UserConfigDir()
}
