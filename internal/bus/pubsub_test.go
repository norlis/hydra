package bus_test

import (
	"log/slog"
	"sync"
	"testing"
	"uuid"

	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
)

func newBus() bus.EventBus { return bus.NewEventBus(slog.New(slog.DiscardHandler)) }

// recv reads one event or fails the test when the context expires.
func recv(t *testing.T, ch <-chan bus.ClusterEvent) bus.ClusterEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-t.Context().Done():
		t.Fatal("no event received")
	}
	return bus.ClusterEvent{}
}

func TestEventBus(t *testing.T) {
	t.Parallel()

	t.Run("delivers to every subscriber", func(t *testing.T) {
		t.Parallel()
		b := newBus()
		a, c := b.Subscribe(), b.Subscribe()
		b.Publish(bus.ClusterEvent{Type: bus.NodeJoined, Node: topology.Node{ID: "n1"}})
		for _, ch := range []<-chan bus.ClusterEvent{a, c} {
			ev := recv(t, ch)
			if ev.Type != bus.NodeJoined || ev.Node.ID != "n1" {
				t.Errorf("event = %+v", ev)
			}
		}
	})

	t.Run("assigns an ID and overrides any provided one", func(t *testing.T) {
		t.Parallel()
		b := newBus()
		ch := b.Subscribe()
		b.Publish(bus.ClusterEvent{ID: "mio", Type: bus.NodeLeft})
		ev := recv(t, ch)
		id, err := uuid.Parse(ev.ID)
		if err != nil || ev.ID == "mio" {
			t.Fatalf("ID = %q, want a generated UUID: %v", ev.ID, err)
		}
		// Time-ordered v7 so SSE clients can sort events by id (RFC 9562 §4.2).
		if version := id[6] >> 4; version != 7 {
			t.Errorf("UUID version = %d, want 7", version)
		}
	})

	t.Run("Unsubscribe closes the channel and a later Publish does not panic", func(t *testing.T) {
		t.Parallel()
		b := newBus()
		ch := b.Subscribe()
		b.Unsubscribe(ch)
		if _, ok := <-ch; ok {
			t.Error("channel still open after Unsubscribe")
		}
		b.Publish(bus.ClusterEvent{Type: bus.NodeUpdated})
	})

	t.Run("Unsubscribe of a foreign channel is a no-op", func(t *testing.T) {
		t.Parallel()
		b := newBus()
		own := b.Subscribe()
		b.Unsubscribe(make(chan bus.ClusterEvent))
		b.Publish(bus.ClusterEvent{Type: bus.NodeJoined})
		recv(t, own)
	})

	t.Run("a subscriber that stops reading loses events and each loss is logged", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		b := bus.NewEventBus(rec.Logger())
		ch := b.Subscribe()
		for range bus.SubscriberBuffer {
			b.Publish(bus.ClusterEvent{Type: bus.NodeJoined, Node: topology.Node{ID: "n1"}})
		}
		if n := len(rec.Entries()); n != 0 {
			t.Fatalf("logged %d entries before the buffer filled", n)
		}
		b.Publish(bus.ClusterEvent{Type: bus.NodeLeft, Node: topology.Node{ID: "n2"}})
		b.Publish(bus.ClusterEvent{Type: bus.NodeLeft, Node: topology.Node{ID: "n3"}})

		if got := len(ch); got != bus.SubscriberBuffer {
			t.Errorf("buffered events = %d, want %d", got, bus.SubscriberBuffer)
		}
		entries := rec.Entries()
		if len(entries) != 2 {
			t.Fatalf("logged %d entries, want one per dropped event: %+v", len(entries), entries)
		}
		e := entries[1]
		if e.Level != slog.LevelWarn || e.String("event") != "node.left" || e.String("node") != "n3" || e.String("dropped") != "2" {
			t.Errorf("entry = %+v, want Warn with event=node.left node=n3 dropped=2", e)
		}
	})

	t.Run("EventType renders its name", func(t *testing.T) {
		t.Parallel()
		cases := map[bus.EventType]string{bus.NodeJoined: "node.joined", bus.NodeLeft: "node.left", bus.NodeUpdated: "node.updated", bus.EventType(99): "node.unknown_event_99"}
		for et, want := range cases {
			if got := et.String(); got != want {
				t.Errorf("%d.String() = %q, want %q", int(et), got, want)
			}
		}
	})
}

func TestEventBus_Concurrent(t *testing.T) {
	t.Parallel()
	b := newBus()
	fixed := b.Subscribe()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 50 {
				b.Publish(bus.ClusterEvent{Type: bus.NodeUpdated})
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			b.Unsubscribe(b.Subscribe())
		}
	})
	wg.Wait()
	if got := len(fixed); got > bus.SubscriberBuffer {
		t.Errorf("fixed subscriber holds %d, want <= buffer", got)
	}
}
