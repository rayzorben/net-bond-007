// Command net-bond-007 is the Net-Bond-007 client.
//
//	net-bond-007                      run (on Windows, double-click installs)
//	net-bond-007 run [--config f]     run in the foreground
//	net-bond-007 install [--server host:port --token t]
//	net-bond-007 uninstall
//	net-bond-007 version
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/rayzorben/net-bond-007/internal/speedtest"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/rayzorben/net-bond-007/internal/app"
	"github.com/rayzorben/net-bond-007/internal/client"
	"github.com/rayzorben/net-bond-007/internal/platform"
	buildversion "github.com/rayzorben/net-bond-007/internal/version"
)

// version comes from internal/version/VERSION, embedded at build time.
var version = buildversion.Version

func main() {
	client.Version = version
	speedtest.UserAgent = "net-bond-007/" + version
	if handled := maybeRunAsService(); handled {
		return
	}
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	} else if len(args) == 0 && runtime.GOOS == "windows" {
		cmd = "double-click"
	}

	fs := flag.NewFlagSet("net-bond-007 "+cmd, flag.ExitOnError)
	cfgPath := fs.String("config", platform.DefaultConfigPath("client"), "config file")
	srv := fs.String("server", "", "server address host:port (saved to config)")
	token := fs.String("token", "", "server access token (saved to config)")
	ui := fs.String("ui", "", "dashboard listen address, e.g. 127.0.0.1:7007 (saved to config)")
	fs.Parse(args)
	ov := app.ClientOverrides{Server: *srv, Token: *token, UIListen: *ui}

	switch cmd {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := app.RunClient(ctx, *cfgPath, ov, os.Stderr); err != nil {
			fatal(err)
		}
	case "install":
		install(*cfgPath, ov)
	case "double-click":
		doubleClick(*cfgPath)
	case "uninstall":
		if err := platform.UninstallService(); err != nil {
			fatal(err)
		}
		fmt.Println("Net-Bond-007 service removed. Settings were kept in", *cfgPath)
	case "version":
		fmt.Printf("net-bond-007 %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (run, install, uninstall, version)\n", cmd)
		os.Exit(2)
	}
}

func install(cfgPath string, ov app.ClientOverrides) {
	if ov.Server != "" || ov.Token != "" || ov.UIListen != "" {
		if err := app.SaveClientOverrides(cfgPath, ov); err != nil {
			fatal(err)
		}
	}
	if err := platform.InstallService(cfgPath); err != nil {
		fatal(err)
	}
	url := "http://127.0.0.1:7007"
	if platform.IsOpenWrt() {
		url = "http://<router-ip>:7007"
	}
	fmt.Println("Net-Bond-007 installed and running.")
	fmt.Println("Open the dashboard:", url)
	platform.OpenBrowser(url)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "net-bond-007:", err)
	if runtime.GOOS == "windows" && len(os.Args) == 1 {
		fmt.Fprintln(os.Stderr, "\nPress Enter to close.")
		fmt.Scanln()
	}
	os.Exit(1)
}
