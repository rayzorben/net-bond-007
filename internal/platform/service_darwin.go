//go:build darwin

package platform

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const plistPath = "/Library/LaunchDaemons/com.net-bond-007.client.plist"

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.net-bond-007.client</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>run</string><string>--config</string><string>%s</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>/var/log/net-bond-007.log</string>
	<key>StandardErrorPath</key><string>/var/log/net-bond-007.log</string>
</dict>
</plist>
`

func IsOpenWrt() bool { return false }

func InstallService(cfgPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install needs administrator rights: run with sudo")
	}
	dst := "/usr/local/bin/net-bond-007"
	if err := copySelf(dst); err != nil {
		return err
	}
	exec.Command("xattr", "-d", "com.apple.quarantine", dst).Run()
	if err := os.WriteFile(plistPath, []byte(fmt.Sprintf(plist, dst, cfgPath)), 0o644); err != nil {
		return err
	}
	exec.Command("launchctl", "bootout", "system/com.net-bond-007.client").Run()
	return run("launchctl", "bootstrap", "system", plistPath)
}

func UninstallService() error {
	exec.Command("launchctl", "bootout", "system/com.net-bond-007.client").Run()
	os.Remove(plistPath)
	os.Remove("/usr/local/bin/net-bond-007")
	return nil
}

func copySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	src, _ = filepath.EvalSymlinks(src)
	if src == dst {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.OpenFile(dst+".new", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(dst+".new", dst)
}

func OpenBrowser(url string) { exec.Command("open", url).Start() }
