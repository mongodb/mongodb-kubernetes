package ommetrics

import (
	"testing"
	"time"
)

func TestBreakerBackoff(t *testing.T) {
	cfg := NewConfig()
	tests := []struct {
		consecFail int32
		want       time.Duration
	}{
		{consecFail: 3, want: 5 * time.Second},
		{consecFail: 4, want: 10 * time.Second},
		{consecFail: 5, want: 20 * time.Second},
		{consecFail: 13, want: 10 * time.Minute}, // shift capped at 10
		{consecFail: 30, want: 10 * time.Minute}, // max caps
	}
	for _, tc := range tests {
		if got := breakerBackoff(tc.consecFail, cfg); got != tc.want {
			t.Fatalf("breakerBackoff(%d) = %v, want %v", tc.consecFail, got, tc.want)
		}
	}
}

func TestDestinationBreakerWithInjectedClock(t *testing.T) {
	cfg := NewConfig()
	d := &destination{key: destKey{base: "b", groupID: "g"}}
	now := int64(0)

	if d.skipExport(now) {
		t.Fatal("breaker open on fresh destination")
	}

	d.recordResult(errFake, now, cfg)
	d.recordResult(errFake, now, cfg)
	if d.skipExport(now) {
		t.Fatal("breaker opened below threshold")
	}

	d.recordResult(errFake, now, cfg)
	if !d.skipExport(now) {
		t.Fatal("breaker did not open at threshold")
	}
	if d.skipExport(now + int64(5*time.Second)) {
		t.Fatal("breaker reopened before backoff elapsed")
	}
	if d.skipExport(now + int64(5*time.Second) + 1) {
		t.Fatal("breaker still open after backoff elapsed")
	}

	d.recordResult(nil, now, cfg)
	if d.skipExport(now) {
		t.Fatal("breaker stayed open after success")
	}
	if d.consecFail.Load() != 0 {
		t.Fatalf("consecFail = %d after success, want 0", d.consecFail.Load())
	}
}

var errFake = &fakeError{}

type fakeError struct{}

func (*fakeError) Error() string { return "fake" }
