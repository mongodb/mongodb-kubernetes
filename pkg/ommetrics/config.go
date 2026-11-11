package ommetrics

import (
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultCollectInterval    = 60 * time.Second
	defaultReaderTimeout      = 45 * time.Second
	defaultPerDestTimeout     = 5 * time.Second
	defaultMaxConcurrentPerOM = 8
	defaultMaxIdleConns       = 100
	defaultBreakerThreshold   = 3
	defaultBreakerBase        = 5 * time.Second
	defaultBreakerMax         = 10 * time.Minute
)

type Config struct {
	CollectInterval time.Duration
	// ReaderTimeout bounds one full fanout export cycle; must be at least PerDestTimeout.
	ReaderTimeout time.Duration
	// PerDestTimeout bounds a single destination's export, including OTLP retries.
	PerDestTimeout time.Duration
	// MaxConcurrentPerOM also sizes MaxIdleConnsPerHost on the per-OM transport.
	MaxConcurrentPerOM int
	MaxIdleConns       int
	BreakerThreshold   int
	BreakerBase        time.Duration
	BreakerMax         time.Duration
	// Resource is shared by all destinations; each merges its deployment mappings on top.
	Resource map[string]string
}

func NewConfig() Config {
	return Config{
		CollectInterval:    defaultCollectInterval,
		ReaderTimeout:      defaultReaderTimeout,
		PerDestTimeout:     defaultPerDestTimeout,
		MaxConcurrentPerOM: defaultMaxConcurrentPerOM,
		MaxIdleConns:       defaultMaxIdleConns,
		BreakerThreshold:   defaultBreakerThreshold,
		BreakerBase:        defaultBreakerBase,
		BreakerMax:         defaultBreakerMax,
	}
}

// resourceAttributes describes the operator identity, identical for all destinations.
func (c Config) resourceAttributes() []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(c.Resource))
	for k, v := range c.Resource {
		attrs = append(attrs, attribute.String(k, v))
	}
	return attrs
}

// validateTimeoutBudget keeps a destination's retries inside the reader timeout, and the reader timeout inside the collect interval.
func (c Config) validateTimeoutBudget() error {
	if c.PerDestTimeout <= 0 {
		return fmt.Errorf("ommetrics: PerDestTimeout must be positive, got %v", c.PerDestTimeout)
	}
	if c.ReaderTimeout < c.PerDestTimeout {
		return fmt.Errorf("ommetrics: ReaderTimeout (%v) must be at least PerDestTimeout (%v); "+
			"a destination's retries must stay inside the reader's budget", c.ReaderTimeout, c.PerDestTimeout)
	}
	if c.CollectInterval <= c.ReaderTimeout {
		return fmt.Errorf("ommetrics: CollectInterval (%v) must exceed ReaderTimeout (%v) "+
			"so one slow cycle cannot overlap the next", c.CollectInterval, c.ReaderTimeout)
	}
	return nil
}
