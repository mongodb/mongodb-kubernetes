package mongodbproxy

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventLogKeepsNewestAndCountsDrops(t *testing.T) {
	l := newEventLog()
	total := maxEvents + 10
	for i := range total {
		l.record(Event{Action: "test", PID: i})
	}

	events, dropped := l.snapshot()
	require.Len(t, events, maxEvents)
	assert.Equal(t, uint64(10), dropped)
	assert.Equal(t, total-1, events[0].PID)
	assert.Equal(t, maxEvents, events[9].PID)
	assert.NotEmpty(t, events[0].Time)
	assert.Equal(t, uint64(total), events[0].Seq)
}

func TestEventLogSanitizesAndTruncatesText(t *testing.T) {
	l := newEventLog()
	l.record(Event{
		Action:      "bad event " + string(make([]byte, maxEventText*2)),
		RequestID:   "bad request id",
		OperationID: "bad id with spaces",
		ErrorClass:  "bad error " + string(make([]byte, maxEventText*2)),
		Returned:    "bad returned " + string(make([]byte, maxEventText*2)),
	})

	events, _ := l.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "[redacted]", events[0].RequestID)
	assert.Equal(t, "[redacted]", events[0].OperationID)
	assert.LessOrEqual(t, len(events[0].Action), maxEventText)
	assert.LessOrEqual(t, len(events[0].ErrorClass), maxEventText)
	assert.LessOrEqual(t, len(events[0].Returned), maxEventText)
}

func TestEventLogKeepsRequestAndReturned(t *testing.T) {
	l := newEventLog()
	l.record(Event{
		Action:    "start",
		RequestID: "req-1",
		Request:   requestStart,
		Outcome:   "success",
		Returned:  "200",
	})

	events, _ := l.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "start", events[0].Action)
	assert.Equal(t, "req-1", events[0].RequestID)
	assert.Equal(t, requestStart, events[0].Request)
	assert.Equal(t, "success", events[0].Outcome)
	assert.Equal(t, "200", events[0].Returned)
}

func TestEventLogDropsUnknownRoles(t *testing.T) {
	l := newEventLog()
	l.record(Event{Action: "start", Role: Role("secret payload")})

	events, _ := l.snapshot()
	require.Len(t, events, 1)
	assert.Empty(t, events[0].Role)
}

func TestEventLogConcurrentRecord(t *testing.T) {
	l := newEventLog()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range 100 {
				l.record(Event{Action: "test", PID: id*100 + j})
			}
		}(i)
	}
	wg.Wait()

	events, _ := l.snapshot()
	assert.Len(t, events, maxEvents)
}
