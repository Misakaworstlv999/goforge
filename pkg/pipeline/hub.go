package pipeline

import "sync"

// eventHub fans a run's event stream out to multiple subscribers and retains the
// full history so a late subscriber can replay what it missed before following
// live. The pipeline's single-consumer iter.Seq2 is forwarded here by the
// Manager, decoupling the run from however many observers attach.
type eventHub struct {
	mu      sync.Mutex
	history []Event
	subs    map[int]chan Event
	next    int
	closed  bool
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[int]chan Event)}
}

// publish appends an event to the history and delivers it to live subscribers.
// Delivery is best-effort (non-blocking): a slow subscriber never stalls the
// run, and the full record is always retained in history for replay.
func (h *eventHub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.history = append(h.history, ev)
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe returns a snapshot of all events so far (replay) plus a channel of
// subsequent events (live), and a cancel func to unsubscribe. Registration and
// the replay snapshot happen under one lock, so the boundary is exact: no event
// is both replayed and delivered live, and none is dropped at the seam. If the
// hub is already closed, the live channel is returned closed (replay only).
func (h *eventHub) subscribe() (replay []Event, live <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	replay = append([]Event(nil), h.history...)
	ch := make(chan Event, 256)
	if h.closed {
		close(ch)
		return replay, ch, func() {}
	}
	id := h.next
	h.next++
	h.subs[id] = ch
	return replay, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
		}
	}
}

// close ends the stream: live subscriber channels are closed (observers see EOF).
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		close(ch)
		delete(h.subs, id)
	}
}
