//go:build !linux

package platform

import "errors"

func PortMapSupported() bool { return false }

func ApplyPortMap(PortMapSpec) error { return errors.New("port forwarding needs a Linux router") }

func ClearPortMap() {}

func EnsurePortMapFirewall(string) error { return nil }
