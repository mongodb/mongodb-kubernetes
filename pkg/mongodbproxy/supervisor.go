package mongodbproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	maxOutputBytes = 64 * 1024
	waitDelay      = 2 * time.Second
)

// Supervisor owns at most one mongod/mongos child. It never connects to the database
// and keeps no state outside of memory.
type Supervisor struct {
	binDir string
	stdout io.Writer
	stderr io.Writer
	log    *zap.SugaredLogger
	// baseEnv is the environment of the child. The package does not read it: main passes the one of the proxy.
	baseEnv []string
	// tailLogs copies the log file of the child to stdout (see logtail.go).
	tailLogs bool
	logPoll  time.Duration

	mu          sync.Mutex
	current     *Execution
	terminating bool
	lastExit    *ExitStatus
	events      *eventLog
}

type Option func(*Supervisor)

// WithBinDir resolves mongod/mongos from dir instead of PATH.
func WithBinDir(dir string) Option {
	return func(s *Supervisor) { s.binDir = dir }
}

// WithOutput sets where the child's stdout and stderr are forwarded to.
func WithOutput(stdout, stderr io.Writer) Option {
	return func(s *Supervisor) { s.stdout, s.stderr = stdout, stderr }
}

// WithEnv sets the environment that the child starts with, as os.Environ returns it. Without it the child has an
// empty one.
func WithEnv(env []string) Option {
	return func(s *Supervisor) { s.baseEnv = env }
}

// WithLogTail sets whether the log file of the child is followed and copied to the proxy's stdout. It is on by default.
func WithLogTail(on bool) Option {
	return func(s *Supervisor) { s.tailLogs = on }
}

// WithLogger sets the logger of the supervisor. Without it nothing is logged.
func WithLogger(log *zap.SugaredLogger) Option {
	return func(s *Supervisor) { s.log = log }
}

// NewSupervisor creates a Supervisor that forwards the child's output to the proxy's stdout/stderr.
func NewSupervisor(opts ...Option) *Supervisor {
	s := &Supervisor{stdout: os.Stdout, stderr: os.Stderr, log: zap.NewNop().Sugar(), tailLogs: true, logPoll: defaultLogTailPoll, events: newEventLog()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Execution is one launched child.
type Execution struct {
	role        Role
	configPath  string
	operationID string
	pid         int
	launchEnv   LaunchEnvironment
	cmd         *exec.Cmd
	output      *tailBuffer
	done        chan struct{}

	startedAt time.Time
	// set under Supervisor.mu before done is closed
	exitCode int
	signal   string
	exitedAt time.Time
}

// Done is closed once the child has exited.
func (e *Execution) Done() <-chan struct{} { return e.done }

func (e *Execution) finished() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func (e *Execution) signalProcess(sig syscall.Signal) error {
	err := e.cmd.Process.Signal(sig)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// Status returns the current/last execution state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(s.current)
}

// StatusOf returns the state of a specific execution.
func (s *Supervisor) StatusOf(e *Execution) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(e)
}

func (s *Supervisor) statusLocked(e *Execution) Status {
	if e == nil {
		return Status{State: StateIdle}
	}
	st := Status{State: StateRunning, Role: e.role, ConfigPath: e.configPath, OperationID: e.operationID}
	if !e.startedAt.IsZero() {
		st.StartedAt = e.startedAt.UTC().Format(time.RFC3339Nano)
	}
	if e.finished() {
		code := e.exitCode
		st.State = StateExited
		st.ExitCode = &code
		st.Signal = e.signal
		st.Output = e.output.String()
		if !e.exitedAt.IsZero() {
			uptime := 0.0
			if !e.startedAt.IsZero() {
				uptime = e.exitedAt.Sub(e.startedAt).Seconds()
			}
			st.LastExit = &ExitStatus{
				ExitCode:      code,
				Signal:        e.signal,
				ExitedAt:      e.exitedAt.UTC().Format(time.RFC3339Nano),
				UptimeSeconds: uptime,
			}
		}
	} else {
		pid := e.pid
		launchEnv := e.launchEnv
		st.PID = &pid
		st.LaunchEnvironment = &launchEnv
		if !e.startedAt.IsZero() {
			st.UptimeSeconds = time.Since(e.startedAt).Seconds()
		}
		st.LastExit = s.lastExit
	}
	return st
}

func (s *Supervisor) recordEvent(e Event) {
	if s.events != nil {
		s.events.record(e)
	}
}

func (s *Supervisor) eventSnapshot(limit int) ([]Event, uint64) {
	if s.events == nil {
		return []Event{}, 0
	}
	return s.events.snapshot(limit)
}

// Start launches the child and returns right after the process was spawned.
// If a child with the same role is already running it is a no-op and returns the running execution.
func (s *Supervisor) Start(req StartRequest) (*Execution, error) {
	spec, err := req.validate(s.baseEnv)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.terminating {
		return nil, ErrShuttingDown
	}
	if cur := s.current; cur != nil && !cur.finished() {
		if cur.role != req.Role {
			return nil, ErrRoleConflict
		}
		return cur, nil
	}

	if err := rejectFork(req.ConfigPath); err != nil {
		return nil, err
	}

	path, err := s.resolve(spec.binary)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSpawn, err)
	}

	name, args := path, spec.args
	if spec.numactl {
		// Interleaving is a tuning that the server image may not ship the tool for, and a database that does not
		// start is worse than one that starts without it.
		if numactl, err := s.resolveOptional(binaryNumactl); err != nil {
			s.log.Warnw("numactl is not available, starting without it", "error", err)
		} else {
			name, args = numactl, append([]string{"--interleave=all", path}, spec.args...)
		}
	}

	// The state of the log file before the process starts tells the tail where its own lines begin.
	var logPath string
	var logInfoBefore os.FileInfo
	if s.tailLogs {
		if logPath = logPathOf(req.ConfigPath); logPath != "" {
			logInfoBefore, _ = os.Stat(logPath)
		}
	}

	output := &tailBuffer{limit: maxOutputBytes}
	// The program is mongod/mongos (optionally behind numactl) and every argument is built by
	// validate() from typed fields; nothing from the request reaches a shell or is used as a flag verbatim.
	cmd := exec.Command(name, args...) //nolint:gosec
	cmd.Env = spec.env
	cmd.Stdout = io.MultiWriter(s.stdout, output)
	cmd.Stderr = io.MultiWriter(s.stderr, output)
	cmd.WaitDelay = waitDelay

	start := cmd.Start
	if spec.disableTHP {
		start = func() error { return startWithTHPDisabled(cmd) }
	}
	if err := start(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSpawn, err)
	}

	e := &Execution{
		role:        req.Role,
		configPath:  req.ConfigPath,
		operationID: req.OperationID,
		pid:         cmd.Process.Pid,
		startedAt:   time.Now().UTC(),
		launchEnv: LaunchEnvironment{
			LdapCaPath:     req.Other.LdapCaPath,
			KerberosKeytab: req.Other.KerberosKeytab,
		},
		cmd:    cmd,
		output: output,
		done:   make(chan struct{}),
	}
	s.current = e
	s.log.Infow("process started", "operation_id", logOperationID(e.operationID), "role", e.role, "configPath", e.configPath, "pid", e.pid)
	s.recordEvent(Event{Event: "child_started", OperationID: e.operationID, Role: e.role, PID: e.pid, Outcome: "started"})
	go s.wait(e)
	if logPath != "" {
		go tailLog(logPath, logInfoBefore, s.stdout, e.done, s.logPoll)
	}
	return e, nil
}

func (s *Supervisor) wait(e *Execution) {
	_ = e.cmd.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	e.exitCode = e.cmd.ProcessState.ExitCode()
	if ws, ok := e.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		e.signal = ws.Signal().String()
	}
	e.exitedAt = time.Now().UTC()
	uptime := 0.0
	if !e.startedAt.IsZero() {
		uptime = e.exitedAt.Sub(e.startedAt).Seconds()
	}
	s.lastExit = &ExitStatus{
		ExitCode:      e.exitCode,
		Signal:        e.signal,
		ExitedAt:      e.exitedAt.UTC().Format(time.RFC3339Nano),
		UptimeSeconds: uptime,
	}
	outcome := "exited"
	if e.exitCode != 0 {
		outcome = "failed"
	}
	s.log.Infow("process exited", "operation_id", logOperationID(e.operationID), "role", e.role, "configPath", e.configPath, "pid", e.pid, "exitCode", e.exitCode, "signal", e.signal, "uptime_s", uptime)
	s.recordEvent(Event{Event: "child_exited", OperationID: e.operationID, Role: e.role, PID: e.pid, Outcome: outcome, DurationMs: uptime * 1000, Signal: e.signal})
	close(e.done)
}

// resolveOptional is resolve for a program that may be missing. Without a bin dir the answer comes from PATH; with
// one, resolve alone does not look at the file system.
func (s *Supervisor) resolveOptional(binary string) (string, error) {
	path, err := s.resolve(binary)
	if err != nil {
		return "", err
	}
	if s.binDir != "" {
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
	}
	return path, nil
}

func (s *Supervisor) resolve(binary string) (string, error) {
	if s.binDir != "" {
		return filepath.Join(s.binDir, binary), nil
	}
	return exec.LookPath(binary)
}

// Stop sends SIGTERM to the running child and waits for it to exit.
//   - already down: success
//   - role set and different from the running child's role: ErrRoleMismatch, nothing is signalled
//   - timeout elapsed without force: ErrStopTimeout, the child is left alone
//   - timeout elapsed with force: SIGKILL, then wait for exit
//   - timeout 0 with force: SIGKILL right away
func (s *Supervisor) Stop(ctx context.Context, role Role, timeout time.Duration, force bool) error {
	s.mu.Lock()
	e := s.current
	s.mu.Unlock()

	if e == nil || e.finished() {
		return nil
	}
	if role != "" && role != e.role {
		return ErrRoleMismatch
	}

	if timeout <= 0 {
		if !force {
			return fmt.Errorf("%w: timeout must be > 0 unless force is set", ErrInvalidRequest)
		}
		return s.kill(ctx, e)
	}

	s.log.Infow("sending SIGTERM", "operation_id", logOperationID(e.operationID), "role", e.role, "pid", e.pid, "timeout", timeout.String(), "force", force)
	if err := e.signalProcess(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling the process: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-e.done:
		return nil
	case <-timer.C:
		if !force {
			return ErrStopTimeout
		}
		return s.kill(ctx, e)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) kill(ctx context.Context, e *Execution) error {
	s.log.Warnw("sending SIGKILL", "operation_id", logOperationID(e.operationID), "role", e.role, "pid", e.pid)
	if err := e.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("killing the process: %w", err)
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Terminate is called on the container's SIGTERM: it refuses new starts, forwards SIGTERM to the
// child and waits for it to exit (or for ctx). SIGKILL is never sent here; the runtime does that
// when the termination grace period ends.
func (s *Supervisor) Terminate(ctx context.Context) error {
	s.mu.Lock()
	s.terminating = true
	e := s.current
	s.mu.Unlock()

	s.recordEvent(Event{Event: "proxy_shutdown", Outcome: "terminating"})

	if e == nil || e.finished() {
		return nil
	}
	s.log.Infow("forwarding SIGTERM to the child", "operation_id", logOperationID(e.operationID), "role", e.role, "pid", e.pid)
	if err := e.signalProcess(syscall.SIGTERM); err != nil {
		return fmt.Errorf("forwarding SIGTERM: %w", err)
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tailBuffer keeps the last `limit` bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if extra := len(b.buf) - b.limit; extra > 0 {
		b.buf = b.buf[extra:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
