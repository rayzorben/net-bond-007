//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"github.com/rayzorben/net-bond-007/internal/app"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// maybeRunAsService runs under the Windows Service Control Manager when
// started by it.
func maybeRunAsService() bool {
	is, err := svc.IsWindowsService()
	if err != nil || !is {
		return false
	}
	svc.Run(platform.ServiceName, handler{})
	return true
}

type handler struct{}

func (handler) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cfg := platform.DefaultConfigPath("client")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--config" {
			cfg = args[i+1]
		}
	}
	logf := openLog(filepath.Dir(cfg))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunClient(ctx, cfg, app.ClientOverrides{}, logf) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				fmt.Fprintln(logf, "fatal:", err)
				return false, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func openLog(dir string) io.Writer {
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "net-bond-007.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard
	}
	if st, _ := f.Stat(); st != nil && st.Size() > 10<<20 {
		f.Truncate(0)
	}
	return f
}

// doubleClick: installing from Explorer. Elevate if needed, install the
// service, and open the dashboard.
func doubleClick(cfgPath string) {
	if platform.IsInstalled() && !platform.IsElevated() {
		platform.OpenBrowser("http://127.0.0.1:7007")
		return
	}
	if !platform.IsElevated() {
		if err := platform.Elevate([]string{"install"}); err != nil {
			fatal(fmt.Errorf("administrator rights are needed to install: %w", err))
		}
		return
	}
	install(cfgPath, app.ClientOverrides{})
}
