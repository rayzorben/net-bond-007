//go:build windows

package platform

import (
	"bytes"
	"os"
	"path/filepath"
)

// prepareTunDriver writes the embedded, WireGuard-signed wintun.dll next to
// the executable, where the Wintun loader looks for it.
func prepareTunDriver() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dst := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, wintunDLL) {
		return nil
	}
	return os.WriteFile(dst, wintunDLL, 0o644)
}
