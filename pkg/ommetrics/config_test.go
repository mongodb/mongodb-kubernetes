package ommetrics

import (
	"testing"
	"time"
)

func TestResourceAttributesPreserveServiceVersion(t *testing.T) {
	cfg := Config{Resource: map[string]string{
		"service.name":    "mongodb-kubernetes-operator",
		"service.version": "2.0.0",
	}}
	attrs := cfg.resourceAttributes()
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		values[string(attr.Key)] = attr.Value.AsString()
	}
	if values["service.version"] != "2.0.0" {
		t.Fatalf("service.version = %q, want %q", values["service.version"], "2.0.0")
	}
}

func TestValidateTimeoutBudget(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Config)
		wantErr bool
	}{
		{name: "defaults valid", mut: func(c *Config) {}},
		{
			name: "reader timeout below per-dest",
			mut: func(c *Config) {
				c.PerDestTimeout = 10 * time.Second
				c.ReaderTimeout = 5 * time.Second
			},
			wantErr: true,
		},
		{
			name: "interval not exceeding reader timeout",
			mut: func(c *Config) {
				c.ReaderTimeout = 30 * time.Second
				c.CollectInterval = 30 * time.Second
			},
			wantErr: true,
		},
		{
			name: "zero per-dest timeout",
			mut: func(c *Config) {
				c.PerDestTimeout = 0
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewConfig()
			tc.mut(&cfg)
			err := cfg.validateTimeoutBudget()
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateTimeoutBudget() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
