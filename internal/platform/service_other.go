//go:build !linux && !darwin && !windows

package platform

import "errors"

func IsOpenWrt() bool             { return false }
func InstallService(string) error { return errors.New("service install not supported on this OS") }
func UninstallService() error     { return errors.New("service install not supported on this OS") }
func OpenBrowser(string)          {}
