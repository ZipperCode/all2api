// Package logstore stores structured gateway events in a JSONL file.
package logstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Event is one admin or proxy event persisted to the log file.
type Event struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	Platform   string    `json:"platform,omitempty"`
	Pool       string    `json:"pool,omitempty"`
	KeyLabel   string    `json:"key_label,omitempty"`
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Attempts   int       `json:"attempts,omitempty"`
	Action     string    `json:"action,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	Error      string    `json:"error,omitempty"`
	RemoteAddr string    `json:"remote_addr,omitempty"`
}

// Query filters log events.
type Query struct {
	Limit    int
	Kind     string
	Level    string
	Platform string
}

// Store appends and reads JSONL events.
type Store struct {
	mu   sync.Mutex
	path string
}

// New constructs a store and creates the parent directory.
func New(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("log path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	_ = f.Close()
	return &Store{path: path}, nil
}

// Path returns the underlying JSONL file path.
func (s *Store) Path() string { return s.path }

// Record appends one event. It intentionally swallows write errors so request
// forwarding does not fail because logging is unavailable.
func (s *Store) Record(e Event) {
	if s == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	_ = s.Append(e)
}

// Append writes one event to the JSONL file.
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

// Read returns newest matching events first.
func (s *Store) Read(q Query) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if !matches(e, q) {
			continue
		}
		events = append(events, e)
		if len(events) > limit*3 {
			events = events[len(events)-limit:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(events)
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

// Clear truncates the log file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.WriteFile(s.path, nil, 0o600)
}

func matches(e Event, q Query) bool {
	if q.Kind != "" && e.Kind != q.Kind {
		return false
	}
	if q.Level != "" && e.Level != q.Level {
		return false
	}
	if q.Platform != "" && e.Platform != q.Platform {
		return false
	}
	return true
}
