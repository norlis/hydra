package bus

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"uuid"
)

// SubscriberBuffer is the per-subscriber channel capacity. It covers a
// startup burst of peers × interfaces with margin; a consumer that falls
// further behind loses events, and every loss is logged.
const SubscriberBuffer = 256

type subscriber struct {
	ch      chan ClusterEvent
	dropped atomic.Uint64
}

// memoryEventBus is the in-memory implementation of EventBus.
type memoryEventBus struct {
	mu          sync.RWMutex
	subscribers []*subscriber
	log         *slog.Logger
}

// NewEventBus creates a new in-memory event bus.
func NewEventBus(log *slog.Logger) EventBus {
	return &memoryEventBus{log: log}
}

// Subscribe creates and returns a new channel for a listener.
func (b *memoryEventBus) Subscribe() <-chan ClusterEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub := &subscriber{ch: make(chan ClusterEvent, SubscriberBuffer)}
	b.subscribers = append(b.subscribers, sub)

	return sub.ch
}

func (b *memoryEventBus) Unsubscribe(ch <-chan ClusterEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, sub := range b.subscribers {
		if sub.ch == ch {
			b.subscribers[i] = b.subscribers[len(b.subscribers)-1]
			b.subscribers = b.subscribers[:len(b.subscribers)-1]
			close(sub.ch)
			return
		}
	}
}

// Publish fans the event out to every active subscriber. The send never
// blocks: a full subscriber drops the event so one stalled SSE client
// cannot hold back the ring, and the drop is logged with a running count
// so it is visible instead of silent.
func (b *memoryEventBus) Publish(event ClusterEvent) {
	event.ID = uuid.NewV7().String()

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subscribers {
		select {
		case sub.ch <- event:
		default:
			b.log.Warn("event bus: subscriber buffer full, event dropped",
				slog.String("event", event.Type.String()),
				slog.String("node", event.Node.ID),
				slog.Uint64("dropped", sub.dropped.Add(1)),
				slog.Int("buffer", SubscriberBuffer),
			)
		}
	}
}
