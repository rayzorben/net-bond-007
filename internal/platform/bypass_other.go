//go:build !linux

package platform

import "errors"

func BypassSupported() bool { return false }

func ApplyBypass(BypassSpec) error {
	return errors.New("bypass routes are only supported on Linux")
}

func ClearBypass() {}
