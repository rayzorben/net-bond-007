// Command bond007-server is the Bond007 aggregation server, normally run in
// the Docker image.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/rayzorben/bond007/internal/app"
	"github.com/rayzorben/bond007/internal/server"
)

var version = "dev"

func main() {
	server.Version = version
	cfg := flag.String("config", envOr("BOND007_CONFIG", "/data/server.json"), "config file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("bond007-server %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunServer(ctx, *cfg, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "bond007-server:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
