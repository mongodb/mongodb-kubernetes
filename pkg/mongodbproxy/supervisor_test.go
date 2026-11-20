package mongodbproxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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
  env) echo "KRB5_KTNAME=$KRB5_KTNAME LDAPTLS_CACERT=$LDAPTLS_CACERT GLIBC_TUNABLES=$GLIBC_TUNABLES SET_THP_DISABLED=$SET_THP_DISABLED MONGODB_CONFIG_OVERRIDE_NOFORK=$MONGODB_CONFIG_OVERRIDE_NOFORK"; exit 0;;
  args) echo "ARGS: $*"; exit 0;;
  run) trap 'exit 0' TERM;;
  ignoreterm) trap '' TERM;;
esac
touch "$2.ready"
while true; do sleep 0.1; done
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
	stdout := &syncBuffer{}
	sup := NewSupervisor(dir, testEnv, nil)
	sup.stdout, sup.stderr = stdout, stdout
	t.Cleanup(func() {
		// make sure no child outlives the test
		_ = stopCurrent(sup, context.Background(), "", 0, true)
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
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := f.sup.Start(tc.req)
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, StateIdle, f.sup.Status().State)
		})
	}

	t.Run("binary not found", func(t *testing.T) {
		sup := NewSupervisor(t.TempDir(), testEnv, nil)
		_, err := sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain})
		assert.ErrorIs(t, err, ErrSpawn)
		assert.Equal(t, StateIdle, sup.Status().State)
	})
}

func TestStartRejectsTheFlagsTheProxyOwns(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "run")

	for _, args := range [][]string{
		{"-f", cfg},
		{"-f=" + cfg},
		{"--config", cfg},
		{"--config=" + cfg},
		{"--fork"},
		{"--fork=true"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{Args: args}})
			assert.ErrorIs(t, err, ErrInvalidRequest)
			assert.Equal(t, StateIdle, f.sup.Status().State, "nothing may have been started")
		})
	}
}

func TestEnvIsPassedToTheChild(t *testing.T) {
	f := newFixture(t)
	e, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("main", "env"),
		Role:       RoleMain,
		Other: Other{
			Env: []string{
				"KRB5_KTNAME=" + filepath.Join(f.dir, "krb5.keytab"),
				"LDAPTLS_CACERT=" + filepath.Join(f.dir, "ldap-ca.pem"),
				"GLIBC_TUNABLES=glibc.pthread.rseq=0",
				"MONGODB_CONFIG_OVERRIDE_NOFORK=0",
			},
		},
	})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Contains(t, f.sup.Status().Output,
		"KRB5_KTNAME="+filepath.Join(f.dir, "krb5.keytab")+" LDAPTLS_CACERT="+filepath.Join(f.dir, "ldap-ca.pem")+
			" GLIBC_TUNABLES=glibc.pthread.rseq=0 SET_THP_DISABLED=")
	assert.Contains(t, f.sup.Status().Output, " MONGODB_CONFIG_OVERRIDE_NOFORK=1")
}

func TestArgsArePassedToTheChild(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "args")
	args := []string{"--configExpand", "rest", "--restore", "--replicaSetConfigShardMaintenanceMode", "--replSet", "cs0", "--configOnly"}
	e, err := f.sup.Start(StartRequest{
		ConfigPath: cfg,
		Role:       RoleMain,
		Other:      Other{Args: args},
	})
	require.NoError(t, err)
	waitDone(t, e)
	assert.Contains(t, f.sup.Status().Output, "ARGS: -f "+cfg+" "+strings.Join(args, " "))
}

func TestEnvValue(t *testing.T) {
	env := []string{"A=1", "B=2=3", "A=4"}
	assert.Equal(t, "4", envValue(env, "A"), "a later entry wins for the child")
	assert.Equal(t, "2=3", envValue(env, "B"))
	assert.Empty(t, envValue(env, "C"))
	assert.Empty(t, envValue([]string{"A"}, "A"))
}

func stopCurrent(s *Supervisor, ctx context.Context, role Role, timeout time.Duration, force bool) error {
	status := s.Status()
	if role == "" {
		role = status.Role
	}
	_, err := s.stopExecution(ctx, status.ExecutionID, role, timeout, force)
	return err
}

func TestStopSendsSIGTERMAndWaits(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "run")

	require.NoError(t, stopCurrent(f.sup, context.Background(), RoleMain, 5*time.Second, false))
	waitDone(t, e)
	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	assert.Equal(t, 0, *st.ExitCode)
}

func TestStopWhenDownIsNoOp(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, stopCurrent(f.sup, context.Background(), RoleMain, time.Second, false), "idle")

	waitDone(t, f.start(RoleMain, "exit:0"))
	require.NoError(t, stopCurrent(f.sup, context.Background(), RoleMain, time.Second, false), "exited")
}

func TestStopWithOtherRoleDoesNotTouchTheChild(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")

	err := stopCurrent(f.sup, context.Background(), RoleEphemeral, time.Second, true)
	assert.ErrorIs(t, err, ErrRoleMismatch)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStopTimeoutWithoutForceLeavesTheChildAlive(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "ignoreterm")

	err := stopCurrent(f.sup, context.Background(), RoleMain, 300*time.Millisecond, false)
	assert.ErrorIs(t, err, ErrStopTimeout)
	assert.Equal(t, StateRunning, f.sup.Status().State)
}

func TestStopTimeoutWithForceKills(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "ignoreterm")

	require.NoError(t, stopCurrent(f.sup, context.Background(), RoleMain, 300*time.Millisecond, true))
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
	require.NoError(t, stopCurrent(f.sup, context.Background(), "", 0, true))
	waitDone(t, e)
	assert.Less(t, time.Since(begin), 2*time.Second)
	assert.Equal(t, "killed", f.sup.Status().Signal)
}

func TestStopZeroTimeoutWithoutForceIsInvalid(t *testing.T) {
	f := newFixture(t)
	f.start(RoleMain, "run")
	err := stopCurrent(f.sup, context.Background(), "", 0, false)
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

func TestStatusCarriesLaunchMetadataWhileRunning(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "run")
	ldapCA := filepath.Join(f.dir, "ca.pem")
	keytab := filepath.Join(f.dir, "krb5.keytab")
	require.NoError(t, os.WriteFile(ldapCA, []byte("ca"), 0o600))
	require.NoError(t, os.WriteFile(keytab, []byte("keytab"), 0o600))
	e, err := f.sup.Start(StartRequest{
		ConfigPath: cfg,
		Role:       RoleMain,
		Other: Other{
			Env: []string{"LDAPTLS_CACERT=" + ldapCA, "KRB5_KTNAME=" + keytab},
		},
	})
	require.NoError(t, err)
	f.waitReady(cfg)

	st := f.sup.Status()
	require.NotNil(t, st.PID)
	assert.Equal(t, e.pid, *st.PID)
	require.NotNil(t, st.LaunchEnvironment)
	assert.Equal(t, ldapCA, st.LaunchEnvironment.LdapCaPath)
	assert.Equal(t, keytab, st.LaunchEnvironment.KerberosKeytab)

	require.NoError(t, stopCurrent(f.sup, context.Background(), RoleMain, 5*time.Second, false))
	waitDone(t, e)

	st = f.sup.Status()
	assert.Nil(t, st.PID, "pid is only present while the child runs")
	assert.Nil(t, st.LaunchEnvironment, "launch environment is only present while the child runs")
}

func TestStatusStartedAtAndLastExit(t *testing.T) {
	f := newFixture(t)
	e := f.start(RoleMain, "exit:3")
	waitDone(t, e)

	st := f.sup.Status()
	assert.Equal(t, StateExited, st.State)
	assert.NotEmpty(t, st.StartedAt)
	require.NotNil(t, st.LastExit)
	assert.Equal(t, 3, st.LastExit.ExitCode)
	assert.NotEmpty(t, st.LastExit.ExitedAt)
	assert.GreaterOrEqual(t, st.LastExit.UptimeSeconds, 0.0)
}

func TestTailBufferKeepsTheLastBytes(t *testing.T) {
	b := &tailBuffer{limit: 5}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defg"))
	assert.Equal(t, "cdefg", b.String())
}

// testEnv is the environment of the processes started by the tests. The package never reads the environment of the
// proxy itself (main does), so the tests give the children a fixed one.
var testEnv = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
