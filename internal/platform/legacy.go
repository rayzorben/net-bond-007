package platform

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The product was renamed to Net-Bond-007. LegacyName is its former name,
// kept only so existing installs keep their settings and data when they
// upgrade. Remove this file once no device runs a pre-rename version.
const LegacyName = "bond" + "007"

// LegacyConfigPath is where the pre-rename version kept its config.
func LegacyConfigPath(role string) string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/" + strings.ToUpper(LegacyName[:1]) + LegacyName[1:] + "/" + role + ".json"
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, strings.ToUpper(LegacyName[:1])+LegacyName[1:], role+".json")
	}
	return "/etc/" + LegacyName + "/" + role + ".json"
}

// MigrateLegacyConfig copies a pre-rename install's config directory (settings,
// usage counters, speed-test history, server history) to the new location
// when cfgPath doesn't exist yet. It reports whether it copied anything.
func MigrateLegacyConfig(cfgPath, role string) (bool, error) {
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil
	}
	old := LegacyConfigPath(role)
	if _, err := os.Stat(old); err != nil {
		return false, nil
	}
	return copyTree(filepath.Dir(old), filepath.Dir(cfgPath))
}

func copyTree(src, dst string) (bool, error) {
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	return err == nil, err
}
