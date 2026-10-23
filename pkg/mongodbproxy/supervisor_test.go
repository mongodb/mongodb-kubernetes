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
  env) echo "KRB5_KTNAME=$KRB5_KTNAME LDAPTLS_CACERT=$LDAPTLS_CACERT GLIBC_TUNABLES=$GLIBC_TUNABLES SET_THP_DISABLED=$SET_THP_DISABLED"; exit 0;;
  args) echo "ARGS: $*"; exit 0;;
  run) trap 'exit 0' TERM;;
  ignoreterm) trap '' TERM;;
esac
touch "$2.ready"
while true; do sleep 0.1; done
`

// fakeNumactl announces itself, drops "--interleave=all" and runs the rest of the command line.
const fakeNumactl = `#!/bin/sh
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
		// make sure no child outlives the test
		_ = sup.Stop(context.Background(), "", 0, true)
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
}

func TestTypedSettingsAreTurnedIntoFlags(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "args")
	e, err := f.sup.Start(StartRequest{
		ConfigPath: cfg,
		Role:       RoleMain,
		Other:      Other{ConfigExpandRest: true, Restore: true, ShardMaintenanceReplSet: "cs0", MagicRestore: true, ConfigOnly: true},
	})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Contains(t, f.sup.Status().Output,
		"ARGS: -f "+cfg+" --configExpand rest --restore --replicaSetConfigShardMaintenanceMode --replSet cs0 --magicRestore --configOnly")
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

func TestStopSendsSIGTERMAndWaits(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "run")

	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, 5*time.Second, false))
	waitDone(t, e)
	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	assert.Equal(t, 0, *st.ExitCode)
}

func TestStopWhenDownIsNoOp(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, time.Second, false), "idle")

	waitDone(t, f.start(RoleMain, "exit:0"))
	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, time.Second, false), "exited")
}

func TestStopWithOtherRoleDoesNotTouchTheChild(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")

	err := f.sup.Stop(context.Background(), RoleEphemeral, time.Second, true)
	assert.ErrorIs(t, err, ErrRoleMismatch)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStopTimeoutWithoutForceLeavesTheChildAlive(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "ignoreterm")

	err := f.sup.Stop(context.Background(), RoleMain, 300*time.Millisecond, false)
	assert.ErrorIs(t, err, ErrStopTimeout)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStopTimeoutWithForceKills(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "ignoreterm")

	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, 300*time.Millisecond, true))
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
	require.NoError(t, f.sup.Stop(context.Background(), "", 0, true))
	waitDone(t, e)
	assert.Less(t, time.Since(begin), 2*time.Second)
	assert.Equal(t, "killed", f.sup.Status().Signal)
}

func TestStopZeroTimeoutWithoutForceIsInvalid(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")
	err := f.sup.Stop(context.Background(), "", 0, false)
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
