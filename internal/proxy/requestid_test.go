package proxy

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestNewRequestID(t *testing.T) {
	t.Parallel()
	a := newRequestID()
	if _, err := uuid.Parse(a); err != nil {
		t.Fatalf("not a uuid: %q (%v)", a, err)
	}
	if b := newRequestID(); a == b {
		t.Errorf("expected distinct ids, got %q twice", a)
	}
}

func TestRequestIDContextRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := contextWithRequestID(context.Background(), "abc")
	if got := requestIDFromContext(ctx); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
	if got := requestIDFromContext(context.Background()); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
