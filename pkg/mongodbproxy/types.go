// Package mongodbproxy implements the process proxy that runs as PID 1 of the `mongodb` container.
//
// The proxy owns at most one direct mongod/mongos child and exposes a localhost HTTP API
// (see NewHandler) that the automation agent uses instead of fork/kill/proc calls.
// The proxy keeps its process state in memory.
package mongodbproxy

import "errors"

// Role identifies the purpose of an execution.
type Role string

const (
	// RoleMain is the regular database member.
	RoleMain Role = "main"
	// RoleEphemeral is a maintenance execution (e.g. oplog resize, KMIP rotation).
	RoleEphemeral Role = "ephemeral"
)

type State string

const (
	// StateIdle means no execution has been started since the proxy started.
	StateIdle State = "idle"
	// StateRunning means the child is alive.
	StateRunning State = "running"
	// StateExited means the last child has exited; ExitCode/Signal are available.
	StateExited State = "exited"
)

// Binary names the proxy is allowed to launch.
const (
	BinaryMongod = "mongod"
	BinaryMongos = "mongos"
)

var (
	// ErrInvalidRequest wraps validation failures of API requests.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrRoleConflict is returned by start when a child with a different role is already running.
	ErrRoleConflict = errors.New("a process with a different role is already running")
	// ErrRoleMismatch is returned by stop when the selected child has a different role than requested.
	ErrRoleMismatch = errors.New("running process has a different role")
	// ErrExecutionMismatch reports that stop did not select the observed execution.
	ErrExecutionMismatch = errors.New("requested execution is no longer current")
	// ErrStopTimeout is returned by stop when the child is still alive after the timeout and force is not set.
	ErrStopTimeout = errors.New("timed out waiting for the process to exit")
	// ErrShuttingDown is returned by start once the proxy has received a termination signal.
	ErrShuttingDown = errors.New("proxy is shutting down")
	// ErrSpawn wraps failures to launch the child.
	ErrSpawn = errors.New("failed to start the process")
)

// Other carries the agent's raw args and env plus the process attributes that the proxy must apply.
//
// Args and Env are passed to the child as they arrive: the agent already derives them from the
// deployment, so the proxy does not interpret them again. The proxy puts "-f <configPath>" in front
// of Args, rejects the args that would take the process away from it, and extends its own environment
// with Env. Nothing is passed to a shell.
type Other struct {
	// Binary is "mongod" (default) or "mongos".
	Binary string `json:"binary,omitempty"`

	// Args are the agent's command-line arguments for the binary.
	Args []string `json:"args,omitempty"`
	// Env is "KEY=value" entries that extend the environment of the proxy.
	Env []string `json:"env,omitempty"`
}

// StartRequest is the body of POST /v1/process/start.
type StartRequest struct {
	ConfigPath  string `json:"configPath"`
	Role        Role   `json:"role"`
	Other       Other  `json:"other"`
	WaitForExit bool   `json:"waitForExit,omitempty"`
}

// StopRequest is the body of POST /v1/process/stop.
type StopRequest struct {
	ExecutionID string `json:"executionId"`
	// TimeoutSeconds is how long to wait for the child to exit after SIGTERM. Must be > 0 unless Force is set.
	TimeoutSeconds float64 `json:"timeoutSeconds"`
	// Force sends SIGKILL after the timeout (immediately when the timeout is 0).
	Force bool `json:"force,omitempty"`
	Role  Role `json:"role"`
}

// LaunchEnvironment contains only the LDAP CA and Kerberos keytab paths supplied to the tracked child at launch.
// It does not contain file contents or other environment values.
type LaunchEnvironment struct {
	LdapCaPath     string `json:"ldapCaPath,omitempty"`
	KerberosKeytab string `json:"kerberosKeytab,omitempty"`
}

// Status is the response of GET /v1/process and of start/stop.
type Status struct {
	State       State  `json:"state"`
	Role        Role   `json:"role,omitempty"`
	ConfigPath  string `json:"configPath,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	// ExitCode is set once the child has exited. It is -1 when the child was killed by a signal.
	ExitCode *int `json:"exitCode,omitempty"`
	// Signal is the name of the signal that terminated the child, if any.
	Signal string `json:"signal,omitempty"`
	// Output is the tail of the child's stdout/stderr, available after exit.
	Output string `json:"output,omitempty"`
	// PID and LaunchEnvironment are present together only for a running child.
	PID               *int               `json:"pid,omitempty"`
	LaunchEnvironment *LaunchEnvironment `json:"launchEnvironment,omitempty"`
}
