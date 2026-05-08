package testutil

import (
	"context"
	"log/slog"
	"sync"
)

// LogCaptor is a custom slog.Handler that captures log records for testing.
type LogCaptor struct {
	mu      sync.Mutex
	records []slog.Record
	level   slog.Level
}

// NewLogCaptor creates a new LogCaptor with the specified minimum level.
func NewLogCaptor(level slog.Level) *LogCaptor {
	return &LogCaptor{level: level}
}

// Handle captures a log record.
func (c *LogCaptor) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

// Enabled reports whether the handler handles records at the given level.
func (c *LogCaptor) Enabled(_ context.Context, level slog.Level) bool {
	return level >= c.level
}

// WithAttrs returns a new handler with the given attributes.
func (c *LogCaptor) WithAttrs(attrs []slog.Attr) slog.Handler {
	return c
}

// WithGroup returns a new handler with the given group.
func (c *LogCaptor) WithGroup(name string) slog.Handler {
	return c
}

// Records returns all captured records.
func (c *LogCaptor) Records() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]slog.Record, len(c.records))
	copy(out, c.records)
	return out
}

// Contains checks if any record contains a key with a value that contains the given substring.
func (c *LogCaptor) Contains(key string, valSubstr string) bool {
	for _, r := range c.records {
		var found bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				if s, ok := a.Value.String(), true; ok && contains(s, valSubstr) {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// HasLevel checks if any record has the given log level.
func (c *LogCaptor) HasLevel(level slog.Level) bool {
	for _, r := range c.records {
		if r.Level == level {
			return true
		}
	}
	return false
}

// CountWithKey returns the number of records that contain the given key.
func (c *LogCaptor) CountWithKey(key string) int {
	count := 0
	for _, r := range c.records {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				count++
			}
			return true
		})
	}
	return count
}

func contains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
