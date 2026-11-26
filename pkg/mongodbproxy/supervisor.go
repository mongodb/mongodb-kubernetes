package mongodbproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	stdout io.Writer
	stderr io.Writer
	log    *zap.SugaredLogger
	// baseEnv is the environment of the child. The package does not read it: main passes the one of the proxy.
	baseEnv []string

	mu          sync.Mutex
	current     *Execution
	terminating bool
}

// NewSupervisor forwards child output to stdout and stderr.
func NewSupervisor(env []string, log *zap.SugaredLogger) *Supervisor {
	if log == nil {
		log = zap.NewNop().Sugar()
	}
	return &Supervisor{
		stdout: os.Stdout, stderr: os.Stderr, log: log,
		baseEnv: env,
	}
}

// Execution is one launched child.
type Execution struct {
	role       Role
	configPath string
	pid        int
	launchEnv  LaunchEnvironment
	cmd        *exec.Cmd
	output     *tailBuffer
	done       chan struct{}

	startedAt time.Time
	// exitCode and signal are set under Supervisor.mu before done is closed.
	exitCode int
	signal   string
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
	pid := e.pid
	st := Status{State: StateRunning, Role: e.role, ConfigPath: e.configPath, PID: &pid}
	if e.finished() {
		st.State = StateExited
		st.ExitCode = &e.exitCode
		st.Signal = e.signal
		st.Output = e.output.String()
	} else {
		launchEnv := e.launchEnv
		st.LaunchEnvironment = &launchEnv
	}
	return st
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

	output := &tailBuffer{limit: maxOutputBytes}
	// Launch mongod/mongos directly with the config path and validated Agent args. Nothing reaches a shell.
	cmd := exec.Command(spec.binary, spec.args...) //nolint:gosec
	cmd.Env = spec.env
	cmd.Stdout = io.MultiWriter(s.stdout, output)
	cmd.Stderr = io.MultiWriter(s.stderr, output)
	cmd.WaitDelay = waitDelay

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSpawn, err)
	}

	e := &Execution{
		role:       req.Role,
		configPath: req.ConfigPath,
		pid:        cmd.Process.Pid,
		startedAt:  time.Now(),
		launchEnv: LaunchEnvironment{
			LdapCaPath:     envValue(spec.env, "LDAPTLS_CACERT"),
			KerberosKeytab: envValue(spec.env, "KRB5_KTNAME"),
		},
		cmd:    cmd,
		output: output,
		done:   make(chan struct{}),
	}
	s.current = e
	s.log.Infow("process started", "role", e.role, "configPath", e.configPath, "pid", e.pid)
	go s.wait(e)
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
	s.log.Infow("process exited", "role", e.role, "configPath", e.configPath, "pid", e.pid, "exitCode", e.exitCode, "signal", e.signal, "uptime_s", time.Since(e.startedAt).Seconds())
	close(e.done)
}

// stopExecution stops the selected child and returns it for the response.
//   - already down: success
//   - PID or role does not match: no signal
//   - timeout elapsed without force: ErrStopTimeout, the child is left alone
//   - timeout elapsed with force: SIGKILL, then wait for exit
//   - timeout 0 with force: SIGKILL right away
func (s *Supervisor) stopExecution(ctx context.Context, pid int, role Role, timeout time.Duration, force bool) (*Execution, error) {
	s.mu.Lock()
	e := s.current
	s.mu.Unlock()

	if e == nil {
		return nil, nil
	}
	if pid != e.pid {
		return nil, ErrPIDMismatch
	}
	if role != e.role {
		return nil, ErrRoleMismatch
	}
	if e.finished() {
		return e, nil
	}
	if timeout <= 0 {
		if !force {
			return e, fmt.Errorf("%w: timeout must be > 0 unless force is set", ErrInvalidRequest)
		}
		return e, s.kill(ctx, e)
	}

	s.log.Infow("sending SIGTERM", "role", e.role, "pid", e.pid, "timeout", timeout.String(), "force", force)
	if err := e.signalProcess(syscall.SIGTERM); err != nil {
		return e, fmt.Errorf("signalling the process: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-e.done:
		return e, nil
	case <-timer.C:
		if !force {
			return e, ErrStopTimeout
		}
		return e, s.kill(ctx, e)
	case <-ctx.Done():
		return e, ctx.Err()
	}
}

func (s *Supervisor) kill(ctx context.Context, e *Execution) error {
	s.log.Warnw("sending SIGKILL", "role", e.role, "pid", e.pid)
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

	if e == nil || e.finished() {
		return nil
	}
	s.log.Infow("forwarding SIGTERM to the child", "role", e.role, "pid", e.pid)
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
