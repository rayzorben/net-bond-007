// Package app wires configuration, logging, the dashboard and the client or
// server together. The command-line entry points and the Windows service
// handler call into it.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/rayzorben/bond007/internal/client"
	"github.com/rayzorben/bond007/internal/config"
	"github.com/rayzorben/bond007/internal/server"
	"github.com/rayzorben/bond007/internal/webui"
)

// Logging builds a logger whose level follows the live config.
func Logging(out io.Writer) (*slog.Logger, *slog.LevelVar, *Ring) {
	lv := new(slog.LevelVar)
	ring := NewRing(1000)
	h := &teeHandler{inner: slog.NewTextHandler(out, &slog.HandlerOptions{Level: lv}), ring: ring}
	return slog.New(h), lv, ring
}

// withLogs adds GET /api/x/logs to a backend.
type withLogs struct {
	webui.Backend
	ring *Ring
}

func (w withLogs) Routes(mux *http.ServeMux) {
	w.Backend.Routes(mux)
	mux.HandleFunc("GET /api/x/logs", func(rw http.ResponseWriter, r *http.Request) {
		webui.WriteJSON(rw, http.StatusOK, w.ring.Entries())
	})
}

// ClientOverrides are applied to the stored config at startup (from flags).
type ClientOverrides struct {
	Server, Token, UIListen string
}

// RunClient runs the client until ctx is cancelled.
func RunClient(ctx context.Context, cfgPath string, ov ClientOverrides, logOut io.Writer) error {
	log, lv, ring := Logging(logOut)
	store, err := config.Load(cfgPath, config.DefaultClient)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	if ov.Server != "" || ov.Token != "" || ov.UIListen != "" {
		err := store.Mutate(func(c *config.Client) {
			if ov.Server != "" {
				c.Server.Address = ov.Server
				c.Server.Enabled = true
			}
			if ov.Token != "" {
				c.Server.Token = ov.Token
			}
			if ov.UIListen != "" {
				c.UI.Listen = ov.UIListen
			}
		})
		if err != nil {
			return err
		}
	}
	lv.Set(ParseLevel(store.Get().Log.Level))
	log.Info("Bond007 client starting", "version", client.Version, "config", cfgPath)

	c := client.New(store, log)
	ui := webui.New(withLogs{client.Backend{C: c}, ring}, log)
	if err := ui.Listen(store.Get().UI.Listen); err != nil {
		log.Error("dashboard unavailable", "listen", store.Get().UI.Listen, "err", err)
	}
	store.Subscribe(func(old, cfg *config.Client) {
		lv.Set(ParseLevel(cfg.Log.Level))
		if old.UI.Listen != cfg.UI.Listen {
			if err := ui.Listen(cfg.UI.Listen); err != nil {
				log.Error("dashboard rebind failed", "listen", cfg.UI.Listen, "err", err)
			}
		}
	})
	c.Start()
	<-ctx.Done()
	log.Info("shutting down")
	c.Close()
	ui.Close()
	return nil
}

// RunServer runs the server until ctx is cancelled. Environment variables
// BOND007_TOKEN, BOND007_LISTEN, BOND007_UI_LISTEN, BOND007_UI_PASSWORD and
// BOND007_NAME seed the config (handy for containers).
func RunServer(ctx context.Context, cfgPath string, logOut io.Writer) error {
	log, lv, ring := Logging(logOut)
	store, err := config.Load(cfgPath, config.DefaultServer)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	env := map[string]func(c *config.Server, v string){
		"BOND007_TOKEN":       func(c *config.Server, v string) { c.Token = v },
		"BOND007_LISTEN":      func(c *config.Server, v string) { c.Listen = v },
		"BOND007_UI_LISTEN":   func(c *config.Server, v string) { c.UI.Listen = v },
		"BOND007_UI_PASSWORD": func(c *config.Server, v string) { c.UI.Password = v },
		"BOND007_NAME":        func(c *config.Server, v string) { c.Name = v },
		"BOND007_DNS":         func(c *config.Server, v string) { c.Egress.DNSUpstream = v },
	}
	if err := store.Mutate(func(c *config.Server) {
		for k, set := range env {
			if v := os.Getenv(k); v != "" {
				set(c, v)
			}
		}
	}); err != nil {
		return err
	}
	lv.Set(ParseLevel(store.Get().Log.Level))
	log.Info("Bond007 server starting", "version", server.Version, "config", cfgPath)

	s, err := server.New(store, log)
	if err != nil {
		return err
	}
	ui := webui.New(withLogs{server.Backend{S: s}, ring}, log)
	if err := ui.Listen(store.Get().UI.Listen); err != nil {
		log.Error("dashboard unavailable", "err", err)
	}
	store.Subscribe(func(old, cfg *config.Server) {
		lv.Set(ParseLevel(cfg.Log.Level))
		if old.UI.Listen != cfg.UI.Listen {
			if err := ui.Listen(cfg.UI.Listen); err != nil {
				log.Error("dashboard rebind failed", "err", err)
			}
		}
	})
	<-ctx.Done()
	log.Info("shutting down")
	s.Close()
	ui.Close()
	return nil
}

// SaveClientOverrides writes flag values into the config file (used by
// "install" before the service starts).
func SaveClientOverrides(cfgPath string, ov ClientOverrides) error {
	store, err := config.Load(cfgPath, config.DefaultClient)
	if err != nil {
		return err
	}
	return store.Mutate(func(c *config.Client) {
		if ov.Server != "" {
			c.Server.Address = ov.Server
			c.Server.Enabled = true
		}
		if ov.Token != "" {
			c.Server.Token = ov.Token
		}
		if ov.UIListen != "" {
			c.UI.Listen = ov.UIListen
		}
	})
}
