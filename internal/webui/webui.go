// Package webui serves the dashboard (an embedded Svelte app) and the JSON
// API shared by client and server: info, live state over Server-Sent Events,
// configuration with schema, and optional password login.
package webui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Backend is implemented by the client and server applications.
type Backend interface {
	Info() any
	// State returns the live snapshot. history: 0 none, n>0 newest n points, -1 all.
	State(history int) any
	Config() any
	Schema() any
	UpdateConfig(raw []byte) error
	Password() string
	RefreshInterval() time.Duration
	// Routes lets the backend add extra authenticated endpoints.
	Routes(mux *http.ServeMux)
}

type Server struct {
	b      Backend
	log    *slog.Logger
	secret []byte
	mux    *http.ServeMux

	mu   sync.Mutex
	srv  *http.Server
	addr string
	err  error
}

func New(b Backend, log *slog.Logger) *Server {
	s := &Server{b: b, log: log, secret: make([]byte, 32)}
	rand.Read(s.secret)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/state", s.auth(s.state))
	mux.HandleFunc("GET /api/events", s.auth(s.events))
	mux.HandleFunc("GET /api/config", s.auth(s.getConfig))
	mux.HandleFunc("PUT /api/config", s.auth(s.putConfig))
	mux.HandleFunc("GET /api/schema", s.auth(s.schema))
	inner := http.NewServeMux()
	b.Routes(inner)
	mux.Handle("/api/x/", s.auth(inner.ServeHTTP))
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: unknown paths serve index.html.
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r.URL.Path = "/"
			}
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
	s.mux = mux
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// Listen (re)binds the dashboard to addr. Calling it with the current address
// is a no-op, so it can be called on every config change.
func (s *Server) Listen(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr == s.addr && s.srv != nil {
		return nil
	}
	// Go enables Multipath TCP on Linux listeners by default (1.24+). Some
	// vendor kernels (e.g. GL.iNet's 5.4 QSDK) ship a half-implemented MPTCP
	// that fails every handshake, so the dashboard would be unreachable.
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		s.err = err
		return err
	}
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s.srv.Shutdown(ctx)
		cancel()
	}
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	s.srv, s.addr, s.err = srv, addr, nil
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("dashboard stopped", "err", err)
		}
	}()
	s.log.Info("dashboard listening", "url", "http://"+displayAddr(ln.Addr().String()))
	return nil
}

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func displayAddr(a string) string {
	if strings.HasPrefix(a, "0.0.0.0:") || strings.HasPrefix(a, "[::]:") {
		return "localhost" + a[strings.LastIndex(a, ":"):]
	}
	return a
}

// ---------------------------------------------------------------- auth

const cookieName = "b7session"

func (s *Server) token(pw string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(pw))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) authed(r *http.Request) bool {
	pw := s.b.Password()
	if pw == "" {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			return hmac.Equal([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(pw))
		}
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.token(pw)))
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		h(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	pw := s.b.Password()
	if pw != "" && !hmac.Equal([]byte(req.Password), []byte(pw)) {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token(pw), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 86400})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- handlers

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"info":          s.b.Info(),
		"authRequired":  s.b.Password() != "",
		"authenticated": s.authed(r),
	})
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.b.State(-1))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		w.Write(b)
		w.Write([]byte("\n\n"))
		fl.Flush()
		return true
	}
	// First event carries full history; later ones only the newest point.
	if !send(s.b.State(-1)) {
		return
	}
	for {
		iv := s.b.RefreshInterval()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(iv):
		}
		if !s.authed(r) || !send(s.b.State(1)) {
			return
		}
	}
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.b.Config())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.b.UpdateConfig(raw); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.b.Config())
}

func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.b.Schema())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// WriteJSON is exported for backend extension routes.
func WriteJSON(w http.ResponseWriter, code int, v any) { writeJSON(w, code, v) }
