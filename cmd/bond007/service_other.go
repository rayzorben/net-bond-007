//go:build !windows

package main

func maybeRunAsService() bool { return false }

func doubleClick(cfgPath string) {}
