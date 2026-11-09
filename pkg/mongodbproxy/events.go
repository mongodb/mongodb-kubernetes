package mongodbproxy

import (
	"sync"
	"time"
)

const (
	// maxEvents bounds the in-memory history. The proxy is a small PID 1 process,
	// so the history must never grow without a limit.
	maxEvents = 256
	// defaultEventLimit is the number of events returned when the caller does not
	// ask for a specific limit.
	defaultEventLimit = 50
	// maxEventText bounds the free-text fields of one event.
	maxEventText = 128
)

// Event is one proxy lifecycle event. It holds metadata only: no request bodies,
// no config contents, and no child output.
type Event struct {
	Seq         uint64  `json:"seq"`
	Time        string  `json:"time"`
	Event       string  `json:"event"`
	OperationID string  `json:"operationId,omitempty"`
	Role        Role    `json:"role,omitempty"`
	PID         int     `json:"pid,omitempty"`
	Outcome     string  `json:"outcome,omitempty"`
	DurationMs  float64 `json:"durationMs,omitempty"`
	Signal      string  `json:"signal,omitempty"`
	ErrorClass  string  `json:"errorClass,omitempty"`
}

// eventsResponse is the body of GET /v1/events.
type eventsResponse struct {
	Events   []Event `json:"events"`
	Dropped  uint64  `json:"dropped"`
	Capacity int     `json:"capacity"`
}

// eventLog is a fixed-size ring of the most recent proxy events.
type eventLog struct {
	mu      sync.Mutex
	events  [maxEvents]Event
	next    int
	count   int
	seq     uint64
	dropped uint64
}

func newEventLog() *eventLog { return &eventLog{} }

func (l *eventLog) record(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	e.Seq = l.seq
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	e.Event = truncateEventText(e.Event)
	e.OperationID = logOperationID(e.OperationID)
	e.ErrorClass = truncateEventText(e.ErrorClass)

	l.events[l.next] = e
	l.next = (l.next + 1) % maxEvents
	if l.count < maxEvents {
		l.count++
	} else {
		l.dropped++
	}
}

// snapshot returns up to limit events, newest first, and the number of events
// that were overwritten since the proxy started.
func (l *eventLog) snapshot(limit int) ([]Event, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if limit <= 0 || limit > maxEvents {
		limit = maxEvents
	}
	if limit > l.count {
		limit = l.count
	}

	out := make([]Event, 0, limit)
	for i := 1; i <= limit; i++ {
		idx := (l.next - i + maxEvents) % maxEvents
		out = append(out, l.events[idx])
	}
	return out, l.dropped
}

func truncateEventText(s string) string {
	if len(s) <= maxEventText {
		return s
	}
	return s[:maxEventText]
}
