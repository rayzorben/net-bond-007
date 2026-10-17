//go:build !linux

package platform

// Pinning needs Linux policy routing; elsewhere a mode switch moves every
// connection, as before.

func PinsSupported() bool     { return false }
func ApplyPins(PinSpec) error { return nil }
func ClearPins()              {}
func LANDevices() []string    { return nil }
