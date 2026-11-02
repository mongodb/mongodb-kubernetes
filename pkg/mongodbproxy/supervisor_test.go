package mongodbproxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMongod behaves according to the content of the config file passed with -f:
//
//	run         - runs until SIGTERM, then exits 0
//	ignoreterm  - ignores SIGTERM
//	exit:<N>    - prints a line and exits with N
//	env         - prints the launch-related environment and exits 0
//	args        - prints its command line and exits 0
//
// Long-running modes create "<config>.ready" once their signal handling is set up.
const fakeMongod = `#!/bin/sh
mode=$(cat "$2")
case "$mode" in
  exit:*) echo "exiting with ${mode#exit:}"; exit "${mode#exit:}";;
  env) echo "KRB5_KTNAME=$KRB5_KTNAME LDAPTLS_CACERT=$LDAPTLS_CACERT GLIBC_TUNABLES=$GLIBC_TUNABLES SET_THP_DISABLED=$SET_THP_DISABLED ST1_INHERITED_ENV=$ST1_INHERITED_ENV"; exit 0;;
  args) echo "ARGS: $*"; exit 0;;
  run) trap 'exit 0' TERM;;
  ignoreterm) trap '' TERM;;
esac
touch "$2.ready"
while true; do sleep 0.1; done
`

// fakeNumactl announces itself, drops "--interleave=all" and runs the rest of the command line.
const fakeNumactl = `#!/bin/sh
if [ "$1" = "--show" ]; then
  exit "${FAKE_NUMACTL_SHOW_EXIT:-0}"
fi
echo "numactl $1"
shift
exec "$@"
`

// syncBuffer is a bytes.Buffer safe for concurrent use by exec's copy goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fixture struct {
	t      *testing.T
	sup    *Supervisor
	dir    string
	stdout *syncBuffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{BinaryMongod, BinaryMongos} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(fakeMongod), 0o755)) //nolint:gosec
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, binaryNumactl), []byte(fakeNumactl), 0o755)) //nolint:gosec
	stdout := &syncBuffer{}
	sup := NewSupervisor(WithBinDir(dir), WithOutput(stdout, stdout))
	t.Cleanup(func() {
		status := sup.Status()
		role := status.Role
		if role == "" {
			role = RoleMain
		}
		_, _ = sup.Stop(context.Background(), StopRequest{
			Role: role, OperationID: status.OperationID, Force: true,
		})
	})
	return &fixture{t: t, sup: sup, dir: dir, stdout: stdout}
}

// config writes a config file with the given behaviour and returns its path.
func (f *fixture) config(name, mode string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, name+".conf")
	require.NoError(f.t, os.WriteFile(p, []byte(mode), 0o600))
	return p
}

func (f *fixture) start(role Role, mode string) *Execution {
	f.t.Helper()
	cfg := f.config(string(role), mode)
	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: role})
	require.NoError(f.t, err)
	if mode == "run" || mode == "ignoreterm" {
		f.waitReady(cfg)
	}
	return e
}

// waitReady waits until the fake mongod using cfg has set up its signal handling.
func (f *fixture) waitReady(cfg string) {
	f.t.Helper()
	require.Eventually(f.t, func() bool { _, err := os.Stat(cfg + ".ready"); return err == nil },
		5*time.Second, 5*time.Millisecond, "fake mongod did not become ready")
}

func waitDone(t *testing.T, e *Execution) {
	t.Helper()
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit in time")
	}
}

func TestIdleBeforeStart(t *testing.T) {
	f := newFixture(t)
	assert.Equal(t, Status{State: StateIdle}, f.sup.Status())
}

func TestStartReturnsAtSpawnAndStatusIsRunning(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")

	st := f.sup.Status()
	assert.Equal(t, StateRunning, st.State)
	assert.Equal(t, RoleMain, st.Role)
	assert.Nil(t, st.ExitCode)
}

func TestStartWhileRunningIsNoOp(t *testing.T) {
	f := newFixture(t)
	first := f.start(RoleMain, "run")
	second := f.start(RoleMain, "run")
	assert.Same(t, first, second)
}

func TestOperationIdentityBindsRoleAndConfig(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "run")
	first, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, OperationID: "op-1"})
	require.NoError(t, err)
	f.waitReady(cfg)

	for _, req := range []StartRequest{
		{ConfigPath: cfg, Role: RoleMain, OperationID: "op-2"},
		{ConfigPath: f.config("other", "run"), Role: RoleMain, OperationID: "op-1"},
		{ConfigPath: cfg, Role: RoleEphemeral, OperationID: "op-1"},
		{ConfigPath: cfg, Role: RoleMain},
	} {
		_, err := f.sup.Start(req)
		assert.ErrorIs(t, err, ErrOperationMismatch)
	}

	retry, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, OperationID: "op-1"})
	require.NoError(t, err)
	assert.Same(t, first, retry)
	assert.Equal(t, "op-1", f.sup.Status().OperationID)
}

func TestCompletedOperationIdentityIsIdempotentAndAllowsNextOperation(t *testing.T) {
	f := newFixture(t)
	first, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("first", "exit:7"), Role: RoleEphemeral, OperationID: "op-1", WaitForExit: true,
	})
	require.NoError(t, err)
	waitDone(t, first)

	retry, err := f.sup.Start(StartRequest{
		ConfigPath: first.configPath, Role: RoleEphemeral, OperationID: "op-1", WaitForExit: true,
	})
	require.NoError(t, err)
	assert.Same(t, first, retry)
	assert.Equal(t, StateExited, f.sup.Status().State)
	require.NotNil(t, f.sup.Status().ExitCode)
	assert.Equal(t, 7, *f.sup.Status().ExitCode)
	_, err = f.sup.Start(StartRequest{
		ConfigPath: f.config("wrong-config", "exit:0"), Role: RoleEphemeral, OperationID: "op-1", WaitForExit: true,
	})
	assert.ErrorIs(t, err, ErrOperationMismatch)
	_, err = f.sup.Start(StartRequest{
		ConfigPath: first.configPath, Role: RoleMain, OperationID: "op-1", WaitForExit: true,
	})
	assert.ErrorIs(t, err, ErrOperationMismatch)

	next, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("second", "exit:0"), Role: RoleEphemeral, OperationID: "op-2", WaitForExit: true,
	})
	require.NoError(t, err)
	waitDone(t, next)
	assert.Equal(t, "op-2", f.sup.Status().OperationID)
}

func TestOperationIdentityProtectsSameRoleReplacementFromStaleStop(t *testing.T) {
	f := newFixture(t)
	first, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("first", "exit:0"), Role: RoleMain, OperationID: "op-1",
	})
	require.NoError(t, err)
	waitDone(t, first)

	second, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("second", "run"), Role: RoleMain, OperationID: "op-2",
	})
	require.NoError(t, err)
	f.waitReady(second.configPath)

	_, err = f.sup.Stop(context.Background(), StopRequest{
		Role: RoleMain, OperationID: "op-1", Force: true,
	})
	assert.ErrorIs(t, err, ErrOperationMismatch)
	assert.Equal(t, StateRunning, f.sup.Status().State)
	assert.Equal(t, "op-2", f.sup.Status().OperationID)
}

func TestStartWithOtherRoleWhileRunningIsRejected(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")

	_, err := f.sup.Start(StartRequest{ConfigPath: f.config("eph", "run"), Role: RoleEphemeral})
	assert.ErrorIs(t, err, ErrRoleConflict)
	assert.Equal(t, RoleMain, f.sup.Status().Role)
}

func TestExitIsRecordedAndProxyKeepsWorking(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "exit:3")
	waitDone(t, e)

	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	require.NotNil(t, st.ExitCode)
	assert.Equal(t, 3, *st.ExitCode)
	assert.Contains(t, st.Output, "exiting with 3")
	assert.Contains(t, f.stdout.String(), "exiting with 3", "child output must be forwarded")

	// the proxy is still usable: a new execution can be started
	f.start(RoleMain, "run")
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStartFailures(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "run")

	tests := map[string]struct {
		req StartRequest
		err error
	}{
		"relative config path": {StartRequest{ConfigPath: "mongod.conf", Role: RoleMain}, ErrInvalidRequest},
		"missing role":         {StartRequest{ConfigPath: cfg}, ErrInvalidRequest},
		"unknown binary":       {StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{Binary: "sh"}}, ErrInvalidRequest},
		"bad replSet name":     {StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{ShardMaintenanceReplSet: "--fork"}}, ErrInvalidRequest},
		"relative keytab":      {StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{KerberosKeytab: "krb5.keytab"}}, ErrInvalidRequest},
		"relative ldap CA":     {StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{LdapCaPath: "ca.pem"}}, ErrInvalidRequest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := f.sup.Start(tc.req)
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, StateIdle, f.sup.Status().State)
		})
	}

	t.Run("binary not found", func(t *testing.T) {
		sup := NewSupervisor(WithBinDir(t.TempDir()))
		_, err := sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain})
		assert.ErrorIs(t, err, ErrSpawn)
		assert.Equal(t, StateIdle, sup.Status().State)
	})
}

func TestTypedSettingsAreTurnedIntoEnvironment(t *testing.T) {
	f := newFixture(t)
	t.Setenv("ST1_INHERITED_ENV", "preserved")
	e, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("main", "env"),
		Role:       RoleMain,
		Other: Other{
			KerberosKeytab: "/data/krb5.keytab",
			LdapCaPath:     "/data/ldap-ca.pem",
			RseqDisabled:   true,
		},
	})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Contains(t, f.sup.Status().Output,
		"KRB5_KTNAME=/data/krb5.keytab LDAPTLS_CACERT=/data/ldap-ca.pem GLIBC_TUNABLES=glibc.pthread.rseq=0 SET_THP_DISABLED=")
	assert.Contains(t, f.sup.Status().Output, "ST1_INHERITED_ENV=preserved")
}

func TestTypedSettingsAreTurnedIntoFlags(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "args")
	e, err := f.sup.Start(StartRequest{
		ConfigPath: cfg,
		Role:       RoleMain,
		Other:      Other{ConfigExpandRest: true, Restore: true, ShardMaintenanceReplSet: "cs0", ConfigOnly: true},
	})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Contains(t, f.sup.Status().Output,
		"ARGS: -f "+cfg+" --configExpand rest --restore --replicaSetConfigShardMaintenanceMode --replSet cs0 --configOnly")
}

func TestNumaInterleaveRunsUnderNumactl(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "args")
	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{NumaInterleave: true}})
	require.NoError(t, err)
	waitDone(t, e)
	out := f.sup.Status().Output
	assert.Contains(t, out, "numactl --interleave=all")
	assert.Contains(t, out, "ARGS: -f "+cfg, "mongod must still receive its own arguments")
}

func TestNumaInterleaveWithoutNumactlFailsToStart(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.dir, binaryNumactl)))
	_, err := f.sup.Start(StartRequest{ConfigPath: f.config("main", "run"), Role: RoleMain, Other: Other{NumaInterleave: true}})
	assert.ErrorIs(t, err, ErrSpawn)
	assert.Equal(t, StateIdle, f.sup.Status().State)
}

func TestNumaPolicyAuto(t *testing.T) {
	for _, tt := range []struct {
		name        string
		showExit    string
		interleaved bool
	}{
		{name: "available", showExit: "0", interleaved: true},
		{name: "unavailable", showExit: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, binaryNumactl), []byte(fakeNumactl), 0o755)) //nolint:gosec
			t.Setenv("FAKE_NUMACTL_SHOW_EXIT", tt.showExit)
			cfg := f.config("main", "args")
			e, err := f.sup.Start(StartRequest{
				ConfigPath: cfg,
				Role:       RoleMain,
				Other:      Other{NumaPolicy: NumaPolicyAuto},
			})
			require.NoError(t, err)
			waitDone(t, e)
			if tt.interleaved {
				assert.Contains(t, f.sup.Status().Output, "numactl --interleave=all")
			} else {
				assert.NotContains(t, f.sup.Status().Output, "numactl --interleave=all")
			}
			assert.Contains(t, f.sup.Status().Output, "ARGS: -f "+cfg)
		})
	}
}

func TestNumaPolicyAutoSkipsUnavailableNumactl(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.dir, binaryNumactl)))
	cfg := f.config("main", "args")
	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{NumaPolicy: NumaPolicyAuto}})
	require.NoError(t, err)
	waitDone(t, e)
	assert.NotContains(t, f.sup.Status().Output, "numactl --interleave=all")
	assert.Contains(t, f.sup.Status().Output, "ARGS: -f "+cfg)
}

func TestNumaInterleaveFalseDoesNotRequireNumactl(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.Remove(filepath.Join(f.dir, binaryNumactl)))
	cfg := f.config("main", "args")
	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{NumaInterleave: false}})
	require.NoError(t, err)
	waitDone(t, e)
	assert.NotContains(t, f.sup.Status().Output, "numactl --interleave=all")
	assert.Contains(t, f.sup.Status().Output, "ARGS: -f "+cfg)
}

func TestStopSendsSIGTERMAndWaits(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "run")

	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, TimeoutSeconds: 5})
	require.NoError(t, err)
	waitDone(t, e)
	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	assert.Equal(t, 0, *st.ExitCode)
}

func TestStopWhenDownIsNoOp(t *testing.T) {
	f := newFixture(t)
	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, TimeoutSeconds: 1})
	require.NoError(t, err, "idle")

	waitDone(t, f.start(RoleMain, "exit:0"))
	_, err = f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, TimeoutSeconds: 1})
	require.NoError(t, err, "exited")
}

func TestStopWithOtherRoleDoesNotTouchTheChild(t *testing.T) {
	for _, tt := range []struct {
		name        string
		activeRole  Role
		requestRole Role
	}{
		{name: "main is not stopped as ephemeral", activeRole: RoleMain, requestRole: RoleEphemeral},
		{name: "ephemeral is not stopped as main", activeRole: RoleEphemeral, requestRole: RoleMain},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.start(tt.activeRole, "run")

			_, err := f.sup.Stop(context.Background(), StopRequest{Role: tt.requestRole, Force: true})
			assert.ErrorIs(t, err, ErrRoleMismatch)
			assert.Equal(t, StateRunning, f.sup.Status().State)
			assert.Equal(t, tt.activeRole, f.sup.Status().Role)
		})
	}
}

func TestStopTimeoutWithoutForceLeavesTheChildAlive(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "ignoreterm")

	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, TimeoutSeconds: 0.3})
	assert.ErrorIs(t, err, ErrStopTimeout)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStopTimeoutWithForceKills(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "ignoreterm")

	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, TimeoutSeconds: 0.3, Force: true})
	require.NoError(t, err)
	waitDone(t, e)
	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	assert.Equal(t, "killed", st.Signal)
	assert.Equal(t, -1, *st.ExitCode)
}

func TestStopZeroTimeoutWithForceKillsImmediately(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "ignoreterm")

	begin := time.Now()
	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain, Force: true})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Less(t, time.Since(begin), 2*time.Second)
	assert.Equal(t, "killed", f.sup.Status().Signal)
}

func TestStopZeroTimeoutWithoutForceIsInvalid(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")
	_, err := f.sup.Stop(context.Background(), StopRequest{Role: RoleMain})
	assert.ErrorIs(t, err, ErrInvalidRequest)
}

func TestTerminateForwardsSIGTERMAndRejectsNewStarts(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "run")

	require.NoError(t, f.sup.Terminate(context.Background()))
	waitDone(t, e)
	assert.Equal(t, 0, *f.sup.Status().ExitCode)

	_, err := f.sup.Start(StartRequest{ConfigPath: f.config("again", "run"), Role: RoleMain})
	assert.ErrorIs(t, err, ErrShuttingDown)
}

func TestTerminateDoesNotKill(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "ignoreterm")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, f.sup.Terminate(ctx), context.DeadlineExceeded)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestTailBufferKeepsTheLastBytes(t *testing.T) {
	b := &tailBuffer{limit: 5}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defg"))
	assert.Equal(t, "cdefg", b.String())
}
