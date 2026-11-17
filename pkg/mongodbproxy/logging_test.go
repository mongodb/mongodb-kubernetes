package mongodbproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func observedFixture(t *testing.T) (*fixture, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, BinaryMongod), []byte(fakeMongod), 0o755)) //nolint:gosec
	stdout := &syncBuffer{}
	sup := NewSupervisor(WithEnv(testEnv), WithBinDir(dir), WithOutput(stdout, stdout), WithLogger(zap.New(core).Sugar()), WithPathPolicy(onlyIn(dir)))
	t.Cleanup(func() { _ = sup.Stop(context.Background(), "", 0, true) })
	return &fixture{t: t, sup: sup, dir: dir, stdout: stdout}, logs
}

func messages(logs *observer.ObservedLogs) []string {
	var out []string
	for _, e := range logs.All() {
		out = append(out, e.Message)
	}
	return out
}

func TestLogsStartAndExit(t *testing.T) {
	f, logs := observedFixture(t)
	e := f.start(RoleMain, "exit:3")
	waitDone(t, e)

	assert.Equal(t, []string{"process started", "process exited"}, messages(logs))
	exited := logs.FilterMessage("process exited").All()[0].ContextMap()
	assert.EqualValues(t, 3, exited["exitCode"])
	assert.EqualValues(t, RoleMain, exited["role"])
	assert.Contains(t, logs.FilterMessage("process started").All()[0].ContextMap(), "pid")
}

func TestLogsGracefulStopWithoutKill(t *testing.T) {
	f, logs := observedFixture(t)
	e := f.start(RoleMain, "run")

	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, 5*time.Second, true))
	waitDone(t, e)

	assert.Equal(t, 1, logs.FilterMessage("sending SIGTERM").Len())
	assert.Equal(t, 0, logs.FilterMessage("sending SIGKILL").Len())
}

func TestLogsForcedStopKills(t *testing.T) {
	f, logs := observedFixture(t)
	e := f.start(RoleMain, "ignoreterm")

	require.NoError(t, f.sup.Stop(context.Background(), RoleMain, 100*time.Millisecond, true))
	waitDone(t, e)

	assert.Equal(t, 1, logs.FilterMessage("sending SIGKILL").Len())
	exited := logs.FilterMessage("process exited").All()[0].ContextMap()
	assert.Equal(t, "killed", exited["signal"])
}
