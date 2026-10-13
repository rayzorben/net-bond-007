package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Validatable is implemented by Client and Server.
type Validatable interface {
	Validate() error
	Normalize()
}

// Store holds the live configuration, persists it atomically and notifies
// subscribers on every change so settings apply without a restart.
type Store[T any] struct {
	mu   sync.RWMutex
	path string
	cur  *T
	subs []func(old, new *T)
}

// Load reads path (creating it with def if missing). def is also the source
// of defaults for fields absent from the file.
func Load[T any](path string, def func() *T) (*Store[T], error) {
	cfg := def()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	if v, ok := any(cfg).(Validatable); ok {
		v.Normalize()
		if err := v.Validate(); err != nil {
			return nil, err
		}
	}
	s := &Store[T]{path: path, cur: cfg}
	if path != "" {
		if err := s.save(cfg); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// NewMemory creates a store without a backing file (tests, ephemeral runs).
func NewMemory[T any](cfg *T) *Store[T] { return &Store[T]{cur: cfg} }

// Path is the file backing the store ("" for memory stores).
func (s *Store[T]) Path() string { return s.path }

// Get returns the current config. Callers must treat it as read-only.
func (s *Store[T]) Get() *T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Clone returns a deep copy safe to modify and pass to Update.
func (s *Store[T]) Clone() *T {
	b, _ := json.Marshal(s.Get())
	var c T
	_ = json.Unmarshal(b, &c)
	return &c
}

// Subscribe registers fn to be called after each successful Update.
func (s *Store[T]) Subscribe(fn func(old, new *T)) {
	s.mu.Lock()
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
}

// Update validates, persists and publishes a new config.
func (s *Store[T]) Update(next *T) error {
	if v, ok := any(next).(Validatable); ok {
		v.Normalize()
		if err := v.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.path != "" {
		if err := s.save(next); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	old := s.cur
	s.cur = next
	subs := append([]func(old, new *T){}, s.subs...)
	s.mu.Unlock()
	for _, fn := range subs {
		fn(old, next)
	}
	return nil
}

// Mutate is a convenience wrapper: clone, modify, update.
func (s *Store[T]) Mutate(fn func(*T)) error {
	c := s.Clone()
	fn(c)
	return s.Update(c)
}

func (s *Store[T]) save(cfg *T) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
