//go:build linux

package platform

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const systemdUnit = `[Unit]
Description=Bond007 multipath bonding client
Documentation=https://github.com/rayzorben/bond007
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run --config %s
Restart=always
RestartSec=2
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

const procdInit = `#!/bin/sh /etc/rc.common
# Bond007 multipath bonding client
START=95
STOP=10
USE_PROCD=1

start_service() {
	procd_open_instance
	procd_set_param command %s run --config %s
	procd_set_param respawn 3600 2 0
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
}
`

func IsOpenWrt() bool { return exists("/etc/openwrt_release") }

// InstallService copies the running binary into place and registers it to
// start at boot. Returns the dashboard hint for the user.
func InstallService(cfgPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install needs root: run with sudo")
	}
	dst := "/usr/local/bin/bond007"
	if IsOpenWrt() {
		dst = "/usr/bin/bond007"
	}
	if err := copySelf(dst); err != nil {
		return err
	}
	if IsOpenWrt() {
		if err := os.WriteFile("/etc/init.d/bond007", []byte(fmt.Sprintf(procdInit, dst, cfgPath)), 0o755); err != nil {
			return err
		}
		run("/etc/init.d/bond007", "enable")
		return run("/etc/init.d/bond007", "restart")
	}
	if !have("systemctl") {
		return fmt.Errorf("no systemd found; run '%s run' from your init system", dst)
	}
	unit := fmt.Sprintf(systemdUnit, dst, cfgPath)
	if err := os.WriteFile("/etc/systemd/system/bond007.service", []byte(unit), 0o644); err != nil {
		return err
	}
	run("systemctl", "daemon-reload")
	return run("systemctl", "enable", "--now", "bond007.service")
}

func UninstallService() error {
	if IsOpenWrt() {
		run("/etc/init.d/bond007", "stop")
		run("/etc/init.d/bond007", "disable")
		os.Remove("/etc/init.d/bond007")
		return nil
	}
	run("systemctl", "disable", "--now", "bond007.service")
	os.Remove("/etc/systemd/system/bond007.service")
	run("systemctl", "daemon-reload")
	os.Remove("/usr/local/bin/bond007")
	return nil
}

func copySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	src, _ = filepath.EvalSymlinks(src)
	if abs, _ := filepath.Abs(dst); strings.EqualFold(abs, src) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

// OpenBrowser is a no-op on Linux (we usually run as root without a session).
func OpenBrowser(string) {}
