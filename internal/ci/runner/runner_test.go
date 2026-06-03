package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExec_DryRunSkipsAndLogs(t *testing.T) {
	var log bytes.Buffer
	r := &Runner{DryRun: true, LogOut: &log, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	if err := r.Exec(context.Background(), "false"); err != nil {
		t.Fatalf("dry-run exec should not fail even on a normally-failing command: %v", err)
	}
	if !strings.Contains(log.String(), "[dry-run] would run: false") {
		t.Errorf("expected dry-run log message, got %q", log.String())
	}
}

func TestExec_RealRunPropagatesError(t *testing.T) {
	r := &Runner{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, LogOut: &bytes.Buffer{}}
	err := r.Exec(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error from `false`, got nil")
	}
	if ExitCode(err) != 1 {
		t.Errorf("expected exit code 1, got %d", ExitCode(err))
	}
}

func TestCapture_TrimsTrailingNewline(t *testing.T) {
	r := &Runner{Stderr: &bytes.Buffer{}}
	out, err := r.Capture(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if out != "hello" {
		t.Errorf("expected %q, got %q", "hello", out)
	}
}

func TestCapture_IgnoresDryRun(t *testing.T) {
	r := &Runner{DryRun: true, Stderr: &bytes.Buffer{}}
	out, err := r.Capture(context.Background(), "echo", "ok")
	if err != nil {
		t.Fatalf("Capture in dry-run should still run: %v", err)
	}
	if out != "ok" {
		t.Errorf("expected %q, got %q", "ok", out)
	}
}

func TestCheckExitCode(t *testing.T) {
	r := &Runner{}
	if err := r.CheckExitCode(context.Background(), "true"); err != nil {
		t.Errorf("expected nil for `true`, got %v", err)
	}
	err := r.CheckExitCode(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error for `false`, got nil")
	}
	if ExitCode(err) != 1 {
		t.Errorf("expected exit code 1, got %d", ExitCode(err))
	}
}
