// Command net-bond-007-server is the Net-Bond-007 aggregation server, normally run in
// the Docker image.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/rayzorben/net-bond-007/internal/speedtest"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/rayzorben/net-bond-007/internal/app"
	"github.com/rayzorben/net-bond-007/internal/server"
	buildversion "github.com/rayzorben/net-bond-007/internal/version"
)

// version comes from internal/version/VERSION, embedded at build time.
var version = buildversion.Version

func main() {
	server.Version = version
	speedtest.UserAgent = "net-bond-007/" + version
	cfg := flag.String("config", envOr("NET_BOND_007_CONFIG", "/data/server.json"), "config file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("net-bond-007-server %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunServer(ctx, *cfg, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "net-bond-007-server:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
