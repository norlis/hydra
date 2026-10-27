// Package testlog provides an in-memory slog.Handler for tests, so a test
// can assert that an error was logged once, with the expected structured
// fields, without parsing formatted output.
package testlog

import (
	"context"
	"log/slog"
	"sync"
)

// Entry is one captured log record with its attributes flattened. Group
// names are joined to the key with a dot ("http.status").
type Entry struct {
	Level   slog.Level
	Message string
	Attrs   map[string]slog.Value
}

// String returns the attribute value rendered as text, or "" when absent.
func (e Entry) String(key string) string {
	v, ok := e.Attrs[key]
	if !ok {
		return ""
	}
	return v.String()
}

// Recorder is a slog.Handler that keeps every record in memory. The zero
// value is not usable; call New.
type Recorder struct {
	mu      *sync.Mutex
	entries *[]Entry
	attrs   []slog.Attr // attrs accumulated through WithAttrs, already prefixed
	prefix  string      // "group." prefix accumulated through WithGroup
}

// New returns an empty Recorder.
func New() *Recorder {
	return &Recorder{mu: &sync.Mutex{}, entries: &[]Entry{}}
}

// Logger returns a slog.Logger writing into the recorder.
func (r *Recorder) Logger() *slog.Logger { return slog.New(r) }

// Entries returns a snapshot of the captured records in emission order.
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(*r.entries))
	copy(out, *r.entries)
	return out
}

// Enabled always reports true: tests want every level captured.
func (r *Recorder) Enabled(context.Context, slog.Level) bool { return true }

// Handle appends the record to the shared entry list.
func (r *Recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]slog.Value, len(r.attrs)+rec.NumAttrs())
	for _, a := range r.attrs {
		flatten(attrs, "", a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		flatten(attrs, r.prefix, a)
		return true
	})
	r.mu.Lock()
	*r.entries = append(*r.entries, Entry{Level: rec.Level, Message: rec.Message, Attrs: attrs})
	r.mu.Unlock()
	return nil
}

// WithAttrs returns a handler sharing the same entry list with extra attrs.
func (r *Recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *r
	next.attrs = make([]slog.Attr, 0, len(r.attrs)+len(attrs))
	next.attrs = append(next.attrs, r.attrs...)
	for _, a := range attrs {
		next.attrs = append(next.attrs, slog.Attr{Key: r.prefix + a.Key, Value: a.Value})
	}
	return &next
}

// WithGroup returns a handler that prefixes subsequent keys with name.
func (r *Recorder) WithGroup(name string) slog.Handler {
	next := *r
	next.prefix = r.prefix + name + "."
	return &next
}

// flatten stores a (possibly grouped) attr under its dotted key.
func flatten(dst map[string]slog.Value, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			flatten(dst, prefix+a.Key+".", sub)
		}
		return
	}
	dst[prefix+a.Key] = a.Value
}
