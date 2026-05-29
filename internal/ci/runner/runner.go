// Package runner wraps os/exec with dry-run support and uniform logging
// of side-effecting commands. Read-only commands (Capture, CheckExitCode)
// always run, since their results drive control flow.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Runner struct {
	DryRun  bool
	WorkDir string
	Stdout  io.Writer
	Stderr  io.Writer
	LogOut  io.Writer
}

func New(dryRun bool, workDir string) *Runner {
	return &Runner{
		DryRun:  dryRun,
		WorkDir: workDir,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		LogOut:  os.Stderr,
	}
}

// Exec runs a command for its side effects, honoring DryRun.
func (r *Runner) Exec(ctx context.Context, name string, args ...string) error {
	if r.DryRun {
		fmt.Fprintf(r.LogOut, "[dry-run] would run: %s\n", format(name, args))
		return nil
	}
	fmt.Fprintf(r.LogOut, "→ %s\n", format(name, args))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.WorkDir
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	return cmd.Run()
}

// Capture runs a command and returns trimmed stdout. Always runs, even
// in dry-run mode, since callers use the output for control-flow decisions.
func (r *Runner) Capture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.WorkDir
	cmd.Stderr = r.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// CheckExitCode runs a command for its exit status only.
// Returns the *exec.ExitError on non-zero exit; nil on success.
// Always runs, regardless of DryRun.
func (r *Runner) CheckExitCode(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.WorkDir
	return cmd.Run()
}

// ExitCode returns the exit code from an error returned by CheckExitCode
// (or any *exec.ExitError). Returns -1 if the error is not an *exec.ExitError.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func format(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}
