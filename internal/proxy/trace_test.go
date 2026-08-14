package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/norlis/httpgate/trace"
)

func TestSetPeerTrace_PropagatesWhenPresent(t *testing.T) {
	t.Parallel()
	tc := trace.New()
	ctx := trace.NewContext(context.Background(), tc)

	h := http.Header{}
	setPeerTrace(h, ctx)

	if got := h.Get(trace.Header); got != tc.Traceparent() {
		t.Errorf("traceparent = %q, want %q", got, tc.Traceparent())
	}
}

func TestSetPeerTrace_NoOpWithoutTrace(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	setPeerTrace(h, context.Background())
	if got := h.Get(trace.Header); got != "" {
		t.Errorf("traceparent = %q, want empty", got)
	}
}

func TestStripControlHeaders_RemovesTraceparent(t *testing.T) {
	t.Parallel()
	f := &DualForwarder{} // stripControlHeaders only touches req.Header
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/", http.NoBody)
	req.Header.Set(trace.Header, "00-"+trace.New().TraceID+"-0000000000000001-01")

	f.stripControlHeaders(req)

	if got := req.Header.Get(trace.Header); got != "" {
		t.Errorf("traceparent still present after strip: %q", got)
	}
}
