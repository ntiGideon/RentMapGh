package messages

import (
	"sync"

	"github.com/google/uuid"
)

// Event tells a user's open pages that something changed; the page then
// fetches what it needs over plain HTTP, so a dropped event loses nothing.
type Event struct {
	Kind         string    `json:"kind"` // "message", "read"
	Conversation uuid.UUID `json:"conversation"`
	Unread       int       `json:"unread"`
}

// Hub fans events out to each user's open streams, in this process. With
// several web instances it would need Postgres LISTEN/NOTIFY; v1 runs one.
type Hub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan Event]struct{}
	done chan struct{}
	once sync.Once
}

func NewHub() *Hub {
	return &Hub{subs: map[uuid.UUID]map[chan Event]struct{}{}, done: make(chan struct{})}
}

// Done is closed on shutdown, ending every open stream.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Close ends all streams (http.Server.RegisterOnShutdown).
func (h *Hub) Close() { h.once.Do(func() { close(h.done) }) }

// Subscribe returns a channel of the user's events and a cancel func.
func (h *Hub) Subscribe(user uuid.UUID) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	h.mu.Lock()
	if h.subs[user] == nil {
		h.subs[user] = map[chan Event]struct{}{}
	}
	h.subs[user][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[user], ch)
		if len(h.subs[user]) == 0 {
			delete(h.subs, user)
		}
		h.mu.Unlock()
	}
}

// Publish sends e to every open stream of the user, dropping it for any
// stream that's too far behind (it will catch up on reconnect).
func (h *Hub) Publish(user uuid.UUID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[user] {
		select {
		case ch <- e:
		default:
		}
	}
}
