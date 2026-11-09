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
		l.record(Event{Event: "test", PID: i})
	}

	events, dropped := l.snapshot(10)
	require.Len(t, events, 10)
	assert.Equal(t, uint64(10), dropped)
	assert.Equal(t, total-1, events[0].PID)
	assert.Equal(t, maxEvents, events[9].PID)
	assert.NotEmpty(t, events[0].Time)
	assert.Equal(t, uint64(total), events[0].Seq)
}

func TestEventLogClampsTheLimit(t *testing.T) {
	l := newEventLog()
	for i := range 3 {
		l.record(Event{Event: "test", PID: i})
	}

	events, _ := l.snapshot(1000)
	assert.Len(t, events, 3)
	events, _ = l.snapshot(0)
	assert.Len(t, events, 3)
}

func TestEventLogSanitizesAndTruncatesText(t *testing.T) {
	l := newEventLog()
	l.record(Event{
		Event:       "bad event " + string(make([]byte, maxEventText*2)),
		OperationID: "bad id with spaces",
		ErrorClass:  "bad error " + string(make([]byte, maxEventText*2)),
	})

	events, _ := l.snapshot(1)
	require.Len(t, events, 1)
	assert.Equal(t, "[redacted]", events[0].OperationID)
	assert.LessOrEqual(t, len(events[0].Event), maxEventText)
	assert.LessOrEqual(t, len(events[0].ErrorClass), maxEventText)
}

func TestEventLogConcurrentRecord(t *testing.T) {
	l := newEventLog()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range 100 {
				l.record(Event{Event: "test", PID: id*100 + j})
			}
		}(i)
	}
	wg.Wait()

	events, _ := l.snapshot(maxEvents)
	assert.Len(t, events, maxEvents)
}
