package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// ParseLevel maps a LOG_LEVEL string (case-insensitive) to a slog.Level.
// Empty or unknown values default to Info.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New builds a production JSON logger writing to stderr. The level is
// intentionally a plain string (not a *Config) so this package stays
// independent of the internal/hydra package.
func New(level string) *slog.Logger {
	return NewWithLevel(ParseLevel(level))
}

// NewWithLevel is New with an explicit slog.Level, for callers that
// need to derive the level programmatically (e.g. the fx event logger).
func NewWithLevel(level slog.Level) *slog.Logger {
	return newWithWriter(level, os.Stderr)
}

// newWithWriter is the testable core. Hybrid schema over slog defaults:
// lowercase level, "timestamp" key instead of "time", and a pid field.
func newWithWriter(level slog.Level, w io.Writer) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.LevelKey:
				if lvl, ok := a.Value.Any().(slog.Level); ok {
					a.Value = slog.StringValue(strings.ToLower(lvl.String()))
				}
			}
			return a
		},
	})
	return slog.New(h).With(slog.Int("pid", os.Getpid()))
}

// Err adapts an error to the canonical "error" attribute (replaces zap.Error).
// slog's JSONHandler renders error values via err.Error().
func Err(e error) slog.Attr {
	return slog.Any("error", e)
}
