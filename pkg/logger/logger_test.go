package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON log line %q: %v", buf.String(), err)
	}
	return m
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"": slog.LevelInfo, "debug": slog.LevelDebug, "info": slog.LevelInfo,
		"INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError,
		"unknown": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWithComponent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	WithComponent(base, "proxy").Info("hi")
	if got := decodeLine(t, &buf)[KeyComponent]; got != "proxy" {
		t.Errorf("%s = %v, want proxy", KeyComponent, got)
	}
}

// Err must produce the standard structured error object, not a flat string.
func TestErr_DelegatesToStandardObject(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Error("boom", Err(errors.New("x")))
	m := decodeLine(t, &buf)
	if m["error.type"] == nil || m["error.message"] == nil {
		t.Errorf("want error.type/error.message from logging.Err, got %v", m)
	}
}
