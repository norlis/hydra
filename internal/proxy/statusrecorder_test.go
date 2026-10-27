package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type flushingRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushingRecorder) Flush() { f.flushed++ }

func TestStatusRecorder(t *testing.T) {
	t.Parallel()

	t.Run("Write without WriteHeader records 200", func(t *testing.T) {
		t.Parallel()
		s := newStatusRecorder(httptest.NewRecorder())
		_, _ = s.Write([]byte("x"))
		if s.status != http.StatusOK {
			t.Errorf("status = %d, want 200", s.status)
		}
	})

	t.Run("WriteHeader(404) records the status", func(t *testing.T) {
		t.Parallel()
		s := newStatusRecorder(httptest.NewRecorder())
		s.WriteHeader(http.StatusNotFound)
		if s.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", s.status)
		}
	})

	t.Run("a second WriteHeader keeps the first status", func(t *testing.T) {
		t.Parallel()
		s := newStatusRecorder(httptest.NewRecorder())
		s.WriteHeader(http.StatusNotFound)
		s.WriteHeader(http.StatusInternalServerError)
		if s.status != http.StatusNotFound {
			t.Errorf("status = %d, want first 404", s.status)
		}
	})

	t.Run("Flush reaches the underlying writer", func(t *testing.T) {
		t.Parallel()
		w := &flushingRecorder{ResponseRecorder: httptest.NewRecorder()}
		newStatusRecorder(w).Flush()
		if w.flushed != 1 {
			t.Errorf("flushed = %d, want 1", w.flushed)
		}
	})
}
