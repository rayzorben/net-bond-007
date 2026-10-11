//go:build !linux && !darwin && !windows

package platform

import (
	"errors"
	"net"
	"syscall"
)

func describe() map[string]hint            { return nil }
func kindOf(ni net.Interface) string       { return kindFromName(ni.Name) }
func DefaultConfigPath(role string) string { return "/etc/bond007/" + role + ".json" }
func TunName(requested string) string      { return requested }

func BindControl(string, int) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return nil }
}

func ConfigureTunnel(TunnelSpec) (func(), error) {
	return func() {}, errors.New("tunnel configuration is not supported on this OS")
}
