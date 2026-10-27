package network_test

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
)

var errDiscover = errors.New("discover boom")

// countingProvider returns two interfaces and can be switched to fail.
type countingProvider struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (p *countingProvider) Discover() ([]topology.NetworkInterface, error) {
	p.calls.Add(1)
	if p.fail.Load() {
		return nil, errDiscover
	}
	return []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1"}, {Name: "eth1", PrivateIP: "10.0.1.1"}}, nil
}

func TestRegistry(t *testing.T) {
	t.Parallel()

	t.Run("discovers on construction and refreshes every 60s", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			p := &countingProvider{}
			r := network.NewRegistry(p, slog.New(slog.DiscardHandler))
			defer r.Stop()
			if p.calls.Load() != 1 || len(r.All()) != 2 {
				t.Fatalf("calls=%d ifaces=%d after construction", p.calls.Load(), len(r.All()))
			}
			time.Sleep(61 * time.Second)
			synctest.Wait()
			if p.calls.Load() != 2 {
				t.Errorf("calls = %d after 61s, want 2", p.calls.Load())
			}
		})
	})

	t.Run("keeps the cache when the provider fails", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			p := &countingProvider{}
			rec := testlog.New()
			r := network.NewRegistry(p, rec.Logger())
			defer r.Stop()
			p.fail.Store(true)
			time.Sleep(61 * time.Second)
			synctest.Wait()
			if len(r.All()) != 2 {
				t.Errorf("cache lost after failed refresh: %d ifaces", len(r.All()))
			}
			errs := 0
			for _, e := range rec.Entries() {
				if e.Level == slog.LevelError {
					errs++
				}
			}
			if errs != 1 {
				t.Errorf("error logs = %d, want 1", errs)
			}
		})
	})

	t.Run("Primary returns the first cached interface and false when empty", func(t *testing.T) {
		t.Parallel()
		r := network.NewRegistry(&countingProvider{}, slog.New(slog.DiscardHandler))
		t.Cleanup(r.Stop)
		if first, ok := r.Primary(); !ok || first.Name != "eth0" {
			t.Errorf("Primary = %+v, %v; want eth0, true", first, ok)
		}
		failing := &countingProvider{}
		failing.fail.Store(true)
		empty := network.NewRegistry(failing, slog.New(slog.DiscardHandler))
		t.Cleanup(empty.Stop)
		if _, ok := empty.Primary(); ok {
			t.Error("Primary on an empty registry = true, want false")
		}
	})

	t.Run("All returns a copy", func(t *testing.T) {
		t.Parallel()
		p := &countingProvider{}
		r := network.NewRegistry(p, slog.New(slog.DiscardHandler))
		t.Cleanup(r.Stop)
		first := r.All()
		first[0].Name = "mutated"
		if r.All()[0].Name != "eth0" {
			t.Error("mutating the returned slice changed the cache")
		}
	})

	t.Run("Stop ends the refresh loop", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			p := &countingProvider{}
			rec := testlog.New()
			r := network.NewRegistry(p, rec.Logger())
			r.Stop()
			time.Sleep(time.Hour)
			synctest.Wait()
			if p.calls.Load() != 1 {
				t.Errorf("calls = %d after Stop, want 1", p.calls.Load())
			}
			found := false
			for _, e := range rec.Entries() {
				if e.Message == "network registry discovery loop stopped" {
					found = true
				}
			}
			if !found {
				t.Error("missing stop log")
			}
		})
	})
}
