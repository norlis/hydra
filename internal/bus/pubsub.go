package bus

import (
	"sync"

	"github.com/google/uuid"
)

// memoryEventBus is the in-memory implementation of EventBus.
type memoryEventBus struct {
	mu          sync.RWMutex
	subscribers []chan ClusterEvent
}

// NewEventBus creates a new in-memory event bus.
func NewEventBus() EventBus {
	return &memoryEventBus{
		subscribers: make([]chan ClusterEvent, 0),
	}
}

// Subscribe creates and returns a new channel for a listener.
func (b *memoryEventBus) Subscribe() <-chan ClusterEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Buffer of 10 to tolerate slightly slow listeners without blocking.
	ch := make(chan ClusterEvent, 10)
	b.subscribers = append(b.subscribers, ch)

	return ch
}

func (b *memoryEventBus) Unsubscribe(ch <-chan ClusterEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, sub := range b.subscribers {
		if sub == ch {
			b.subscribers[i] = b.subscribers[len(b.subscribers)-1]
			b.subscribers = b.subscribers[:len(b.subscribers)-1]
			close(sub)
			return
		}
	}
}

// Publish fans the event out to every active subscriber safely.
func (b *memoryEventBus) Publish(event ClusterEvent) {
	if id, err := uuid.NewV7(); err == nil {
		event.ID = id.String()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		// Non-blocking send: drop if the channel is full to keep the bus alive.
		select {
		case ch <- event:
		default:
		}
	}
}
