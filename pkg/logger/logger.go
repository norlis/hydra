// Package logger is Hydra's thin logging facade over the platform standard
// (github.com/norlis/httpgate/logging). It adds only Hydra domain helpers;
// the logger itself is built with logging.New in cmd/hydra.
package logger

import (
	"log/slog"
	"strings"

	"github.com/norlis/httpgate/logging"
)

// KeyComponent is the Hydra domain field distinguishing data-plane and
// control-plane logs (not covered by the platform standard).
const KeyComponent = "component"

// WithComponent stamps component=name on every record so data-plane and
// control-plane logs can be filtered apart.
func WithComponent(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String(KeyComponent, name))
}

// Err renders err as the platform-standard error object
// (error.type / error.message / error.stack_trace). Kept as a delegate so
// existing call sites (logger.Err) need no change.
func Err(e error) slog.Attr { return logging.Err(e) }

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
