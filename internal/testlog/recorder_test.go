package testlog_test

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/norlis/hydra/internal/testlog"
)

func TestRecorder(t *testing.T) {
	t.Parallel()

	t.Run("keeps With attributes on every entry", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		rec.Logger().With("component", "proxy").Info("x", "k", "v")

		entries := rec.Entries()
		if len(entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(entries))
		}
		if got := entries[0].String("component"); got != "proxy" {
			t.Errorf("component = %q, want %q", got, "proxy")
		}
		if got := entries[0].String("k"); got != "v" {
			t.Errorf("k = %q, want %q", got, "v")
		}
		if entries[0].Level != slog.LevelInfo || entries[0].Message != "x" {
			t.Errorf("entry = %+v", entries[0])
		}
	})

	t.Run("flattens groups as a dotted prefix", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		rec.Logger().WithGroup("http").Info("x", "status", 200)

		if got := rec.Entries()[0].String("http.status"); got != "200" {
			t.Errorf("http.status = %q, want %q", got, "200")
		}
	})

	t.Run("missing key renders as empty string", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		rec.Logger().Warn("x")

		if got := rec.Entries()[0].String("nope"); got != "" {
			t.Errorf("nope = %q, want empty", got)
		}
	})

	t.Run("is safe for concurrent writers", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		log := rec.Logger()
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() { log.Info("x") })
		}
		wg.Wait()

		if got := len(rec.Entries()); got != 100 {
			t.Errorf("entries = %d, want 100", got)
		}
	})
}
