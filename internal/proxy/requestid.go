package proxy

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/norlis/hydra/pkg/logger"
)

type requestIDKeyType struct{}

var requestIDKey requestIDKeyType

// newRequestID returns a per-request correlation id (UUIDv7, time-ordered),
// falling back to v4 only if v7 generation fails (extremely rare).
func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

func contextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// reqLog stamps request_id on base when present in ctx, else returns base
// unchanged. Shared by router and forwarder so every per-request line
// correlates under one id.
func reqLog(base *slog.Logger, ctx context.Context) *slog.Logger {
	if id := requestIDFromContext(ctx); id != "" {
		return base.With(slog.String(logger.KeyRequestID, id))
	}
	return base
}
