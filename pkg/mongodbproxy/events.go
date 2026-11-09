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

const (
	requestStart = "POST /v1/process/start"
	requestStop  = "POST /v1/process/stop"
)

// Event is one trace point of the proxy. Request identifies the HTTP call that
// caused the action, Action names what the proxy did, Outcome and Returned give
// the result, and OperationID, Role and PID tie child events to the request.
// It holds metadata only: no request bodies, no config contents, and no child output.
type Event struct {
	Seq         uint64  `json:"seq"`
	Time        string  `json:"time"`
	RequestID   string  `json:"requestId,omitempty"`
	Request     string  `json:"request,omitempty"`
	Action      string  `json:"action,omitempty"`
	OperationID string  `json:"operationId,omitempty"`
	Role        Role    `json:"role,omitempty"`
	PID         int     `json:"pid,omitempty"`
	Outcome     string  `json:"outcome,omitempty"`
	Returned    string  `json:"returned,omitempty"`
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
	e.RequestID = logOperationID(e.RequestID)
	e.Request = truncateEventText(e.Request)
	e.Action = truncateEventText(e.Action)
	e.OperationID = logOperationID(e.OperationID)
	e.Outcome = truncateEventText(e.Outcome)
	e.Returned = truncateEventText(e.Returned)
	e.ErrorClass = truncateEventText(e.ErrorClass)
	// The role is accepted from the request before validation. Store only the known roles.
	if e.Role != RoleMain && e.Role != RoleEphemeral {
		e.Role = ""
	}

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
