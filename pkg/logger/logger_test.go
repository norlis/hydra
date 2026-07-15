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

func TestNewEmitsHybridSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newWithWriter(slog.LevelInfo, &buf)

	log.Info("hello")

	m := decodeLine(t, &buf)
	if m["level"] != "info" {
		t.Errorf("level = %v, want lowercase %q", m["level"], "info")
	}
	if _, ok := m["timestamp"]; !ok {
		t.Error("missing timestamp key")
	}
	if _, ok := m["time"]; ok {
		t.Error("slog default time key must be renamed to timestamp")
	}
	if _, ok := m["pid"].(float64); !ok {
		t.Errorf("pid = %v, want numeric", m["pid"])
	}
	if _, ok := m["source"]; !ok {
		t.Error("missing source key (AddSource must be enabled)")
	}
	if m["msg"] != "hello" {
		t.Errorf("msg = %v, want %q", m["msg"], "hello")
	}
}

func TestErrAttr(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newWithWriter(slog.LevelInfo, &buf)

	log.Error("boom happened", Err(errors.New("boom")))

	m := decodeLine(t, &buf)
	if m["level"] != "error" {
		t.Errorf("level = %v, want %q", m["level"], "error")
	}
	if m["error"] != "boom" {
		t.Errorf("error = %v, want %q", m["error"], "boom")
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
		"unknown": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newWithWriter(slog.LevelInfo, &buf)

	log.Debug("hidden")

	if buf.Len() != 0 {
		t.Errorf("debug line must be suppressed at info level, got %q", buf.String())
	}
}

// Every level must serialize lowercase, not just info/error — the
// ReplaceAttr lowercasing path is shared, so debug and warn close the gap.
func TestAllLevelsLowercase(t *testing.T) {
	t.Parallel()
	cases := map[slog.Level]string{
		slog.LevelDebug: "debug",
		slog.LevelWarn:  "warn",
	}
	for lvl, want := range cases {
		var buf bytes.Buffer
		log := newWithWriter(slog.LevelDebug, &buf)
		log.Log(t.Context(), lvl, "msg")
		if got := decodeLine(t, &buf)["level"]; got != want {
			t.Errorf("level for %v = %v, want %q", lvl, got, want)
		}
	}
}

// The len(groups)>0 guard must leave nested attrs untouched, so httpgate's
// http.* group is never rewritten. A nested "time"/"level" key stays verbatim.
func TestGroupedAttrsNotRewritten(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newWithWriter(slog.LevelInfo, &buf)

	log.Info("msg", slog.Group("http", slog.String("time", "raw"), slog.String("level", "RAW")))

	m := decodeLine(t, &buf)
	http, ok := m["http"].(map[string]any)
	if !ok {
		t.Fatalf("http group missing or wrong type: %v", m["http"])
	}
	if http["time"] != "raw" {
		t.Errorf("nested time = %v, want %q (guard must skip grouped attrs)", http["time"], "raw")
	}
	if http["level"] != "RAW" {
		t.Errorf("nested level = %v, want %q (guard must not lowercase grouped attrs)", http["level"], "RAW")
	}
}
