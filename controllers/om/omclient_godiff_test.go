package om

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLogDeploymentDiff(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core).Sugar()

	original := Deployment{"processes": []any{map[string]any{"version": "1.0"}}}
	updated := Deployment{"processes": []any{map[string]any{"version": "2.0"}}}
	if err := logDeploymentDiff(logger, original, updated); err != nil {
		t.Fatalf("logDeploymentDiff returned an error: %v", err)
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected one diff log entry, got %d", len(entries))
	}
	message := entries[0].Message
	if !strings.Contains(message, "Deployment diff (1 changes)") || !strings.Contains(message, "version") || !strings.Contains(message, "1.0") || !strings.Contains(message, "2.0") {
		t.Fatalf("diff log did not represent the Deployment change: %s", message)
	}
}
