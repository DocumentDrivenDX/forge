package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// Logger writes session events to a JSONL file.
type Logger struct {
	dir       string
	sessionID string
	mu        sync.Mutex
	file      *os.File
	seq       int
	warned    bool
}

// NewLogger creates a Logger that writes to the given directory.
// If the directory does not exist, it is created. If creation fails,
// the logger will silently skip writes (best-effort logging).
func NewLogger(dir, sessionID string) *Logger {
	l := &Logger{
		dir:       dir,
		sessionID: sessionID,
	}
	if err := safefs.MkdirAll(dir, 0o750); err != nil {
		slog.Warn("session logger: cannot create directory", "dir", dir, "err", err)
		l.warned = true
		return l
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := safefs.Create(path)
	if err != nil {
		slog.Warn("session logger: cannot create file", "path", path, "err", err)
		l.warned = true
		return l
	}
	l.file = f
	return l
}

// Callback returns an EventCallback suitable for agent.Request.Callback.
func (l *Logger) Callback() agent.EventCallback {
	return func(e agent.Event) {
		l.Write(e)
	}
}

// Emit creates and writes an event with auto-incrementing sequence number.
func (l *Logger) Emit(eventType agent.EventType, data any) {
	l.mu.Lock()
	seq := l.seq
	l.seq++
	l.mu.Unlock()

	event := NewEvent(l.sessionID, seq, eventType, data)
	l.Write(event)
}

// Write appends a pre-built event to the log file.
func (l *Logger) Write(e agent.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return
	}

	line, err := json.Marshal(e)
	if err != nil {
		if !l.warned {
			slog.Warn("session logger: marshal error", "err", err)
			l.warned = true
		}
		return
	}
	line = append(line, '\n')
	if _, err := l.file.Write(line); err != nil {
		if !l.warned {
			slog.Warn("session logger: write error", "err", err)
			l.warned = true
		}
	}
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// ReadEvents reads all events from a session log file.
func ReadEvents(path string) ([]agent.Event, error) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: reading log: %w", err)
	}

	var events []agent.Event
	dec := json.NewDecoder(jsonlReader(data))
	for dec.More() {
		var e agent.Event
		if err := dec.Decode(&e); err != nil {
			return events, fmt.Errorf("session: decoding event: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}
