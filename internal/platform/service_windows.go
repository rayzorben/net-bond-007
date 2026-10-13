//go:build windows

package platform

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const ServiceName = "Net-Bond-007"

func IsOpenWrt() bool { return false }

func installDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "Net-Bond-007")
}

// IsElevated reports whether we run with administrator rights.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// Elevate relaunches the current executable as administrator (UAC prompt).
func Elevate(args []string) error {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(args, " "))
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, params, cwd, windows.SW_NORMAL)
}

// IsInstalled reports whether the service exists.
func IsInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

func InstallService(cfgPath string) error {
	if !IsElevated() {
		return fmt.Errorf("install needs administrator rights")
	}
	dst := filepath.Join(installDir(), "net-bond-007.exe")
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(ServiceName); err == nil {
		s.Control(svc.Stop)
		time.Sleep(2 * time.Second)
		s.Delete()
		s.Close()
		time.Sleep(time.Second)
	}
	if err := copySelf(dst); err != nil {
		return err
	}
	if err := prepareTunDriverAt(filepath.Dir(dst)); err != nil {
		return err
	}
	s, err := m.CreateService(ServiceName, dst, mgr.Config{
		DisplayName:      "Net-Bond-007 Multipath Bonding",
		Description:      "Bonds all of your network connections into one fast, reliable link.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: false,
	}, "run", "--config", cfgPath)
	if err != nil {
		return err
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
	}, 86400)
	return s.Start()
}

func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return nil
	}
	s.Control(svc.Stop)
	time.Sleep(2 * time.Second)
	err = s.Delete()
	s.Close()
	return err
}

func copySelf(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func prepareTunDriverAt(dir string) error {
	return os.WriteFile(filepath.Join(dir, "wintun.dll"), wintunDLL, 0o644)
}

func OpenBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
}
