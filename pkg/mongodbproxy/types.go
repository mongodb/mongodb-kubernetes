// Package mongodbproxy implements the process proxy that runs as PID 1 of the `mongodb` container.
//
// The proxy owns at most one direct mongod/mongos child and exposes a localhost HTTP API
// (see NewHandler) that the automation agent uses instead of fork/kill/proc calls.
// The proxy keeps no state outside of memory: the last execution and a bounded event history.
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
	// ErrRoleMismatch is returned by stop when the running child has a different role than requested.
	ErrRoleMismatch = errors.New("running process has a different role")
	// ErrStopTimeout is returned by stop when the child is still alive after the timeout and force is not set.
	ErrStopTimeout = errors.New("timed out waiting for the process to exit")
	// ErrShuttingDown is returned by start once the proxy has received a termination signal.
	ErrShuttingDown = errors.New("proxy is shutting down")
	// ErrSpawn wraps failures to launch the child.
	ErrSpawn = errors.New("failed to start the process")
)

// Other carries typed launch settings. The proxy turns them into command-line flags, environment
// variables and process attributes itself, so a request can never inject arbitrary flags or
// environment variables (and nothing is ever passed to a shell). Everything else mongod needs
// is written by the agent into the config file given by StartRequest.ConfigPath.
//
// The fields mirror what the automation agent adds on top of "-f <config>" when it starts
// mongod/mongos itself (mongoctl.NewStartProcessCmd, util.StartMongoCmdWrapper).
type Other struct {
	// Binary is "mongod" (default) or "mongos".
	Binary string `json:"binary,omitempty"`

	// ConfigExpandRest adds "--configExpand rest" (local configuration server).
	ConfigExpandRest bool `json:"configExpandRest,omitempty"`
	// Restore adds "--restore" (restore from a selective backup).
	Restore bool `json:"restore,omitempty"`
	// ConfigOnly adds "--configOnly".
	ConfigOnly bool `json:"configOnly,omitempty"`
	// ShardMaintenanceReplSet, when set, adds "--replicaSetConfigShardMaintenanceMode --replSet <name>".
	ShardMaintenanceReplSet string `json:"shardMaintenanceReplSet,omitempty"`

	// KerberosKeytab sets KRB5_KTNAME to this absolute path.
	KerberosKeytab string `json:"kerberosKeytab,omitempty"`
	// LdapCaPath sets LDAPTLS_CACERT to this absolute path.
	LdapCaPath string `json:"ldapCaPath,omitempty"`
	// RseqDisabled sets GLIBC_TUNABLES=glibc.pthread.rseq=0 (needed for MongoDB 8.0+).
	RseqDisabled bool `json:"rseqDisabled,omitempty"`
	// DisableTHP disables transparent huge pages for the child (prctl PR_SET_THP_DISABLE, Linux only)
	// and sets SET_THP_DISABLED=yes.
	DisableTHP bool `json:"disableTHP,omitempty"`
	// NumaInterleave runs the child under "numactl --interleave=all".
	NumaInterleave bool `json:"numaInterleave,omitempty"`
	// NumaPolicy is the Agent's NUMA policy. "auto" runs the child under
	// "numactl --interleave=all" when numactl is available, matching the Agent's
	// direct-process path. The Agent sends "auto" for every start request.
	NumaPolicy string `json:"numaPolicy,omitempty"`
}

// StartRequest is the body of POST /v1/process/start.
type StartRequest struct {
	ConfigPath  string `json:"configPath"`
	Role        Role   `json:"role"`
	OperationID string `json:"operationId,omitempty"`
	Other       Other  `json:"other"`
	WaitForExit bool   `json:"waitForExit,omitempty"`
	// RequestID is the internal request identity of the event log. It is not part of the wire format.
	RequestID string `json:"-"`
}

// StopRequest is the body of POST /v1/process/stop.
type StopRequest struct {
	// TimeoutSeconds is how long to wait for the child to exit after SIGTERM. Must be > 0 unless Force is set.
	TimeoutSeconds float64 `json:"timeoutSeconds"`
	// Force sends SIGKILL after the timeout (immediately when the timeout is 0).
	Force bool `json:"force,omitempty"`
	// Role restricts the stop to an execution with this role. Empty matches any.
	Role        Role   `json:"role,omitempty"`
	OperationID string `json:"operationId,omitempty"`
}

// LaunchEnvironment contains only the LDAP CA and Kerberos keytab paths supplied
// to the tracked child at launch. It does not contain file contents or other
// environment values.
type LaunchEnvironment struct {
	LdapCaPath     string `json:"ldapCaPath,omitempty"`
	KerberosKeytab string `json:"kerberosKeytab,omitempty"`
}

// ExitStatus describes the last child that exited.
type ExitStatus struct {
	ExitCode      int     `json:"exitCode"`
	Signal        string  `json:"signal,omitempty"`
	ExitedAt      string  `json:"exitedAt,omitempty"`
	UptimeSeconds float64 `json:"uptimeSeconds,omitempty"`
}

// Status is the response of GET /v1/process and of start/stop.
type Status struct {
	State      State  `json:"state"`
	Role       Role   `json:"role,omitempty"`
	ConfigPath string `json:"configPath,omitempty"`
	// ExitCode is set once the child has exited. It is -1 when the child was killed by a signal.
	ExitCode *int `json:"exitCode,omitempty"`
	// Signal is the name of the signal that terminated the child, if any.
	Signal string `json:"signal,omitempty"`
	// Output is the tail of the child's stdout/stderr, available after exit.
	Output string `json:"output,omitempty"`
	// OperationID is the identity supplied at start, if any.
	OperationID string `json:"operationId,omitempty"`
	// StartedAt is when the current or last child was launched, in RFC3339Nano.
	StartedAt string `json:"startedAt,omitempty"`
	// UptimeSeconds is the running time of the current child.
	UptimeSeconds float64 `json:"uptimeSeconds,omitempty"`
	// LastExit describes the most recent child that exited, if any.
	LastExit *ExitStatus `json:"lastExit,omitempty"`
	// PID and LaunchEnvironment are present together only for a running child.
	// Their absence in a running response identifies a legacy proxy.
	PID               *int               `json:"pid,omitempty"`
	LaunchEnvironment *LaunchEnvironment `json:"launchEnvironment,omitempty"`
}
