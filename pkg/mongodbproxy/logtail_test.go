package mongodbproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTailPoll = 10 * time.Millisecond

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// startTail runs tailLog like the supervisor does: startInfo is the state of the file before the process starts.
func startTail(t *testing.T, path string) (out *syncBuffer, stop func()) {
	t.Helper()
	info, _ := os.Stat(path)
	out = &syncBuffer{}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		tailLog(anyFiles{}, path, info, out, done, testTailPoll)
		close(finished)
	}()
	return out, func() {
		close(done)
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("the tail did not stop")
		}
	}
}

func eventually(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return strings.Contains(out.String(), want) }, 5*time.Second, 5*time.Millisecond,
		"output so far:\n%s", out.String())
}

func TestTailCopiesNewLinesAndSkipsTheOnesFromAnEarlierRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongod.log")
	appendTo(t, path, "from an earlier run\n")
	out, stop := startTail(t, path)

	appendTo(t, path, "first\nsecond\n")
	eventually(t, out, "second\n")
	stop()

	assert.Equal(t, "first\nsecond\n", out.String())
}

func TestTailReadsAFileThatAppearsLaterFromItsStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongod.log")
	out, stop := startTail(t, path)

	appendTo(t, path, "started\n")
	eventually(t, out, "started\n")
	stop()

	assert.Equal(t, "started\n", out.String())
}

func TestTailFollowsAReplacedFile(t *testing.T) {
	// mongod renames the old log when it starts, and a log rotation renames the current one
	path := filepath.Join(t.TempDir(), "mongod.log")
	appendTo(t, path, "old\n")
	out, stop := startTail(t, path)

	appendTo(t, path, "before the rotation\n")
	eventually(t, out, "before the rotation\n")
	require.NoError(t, os.Rename(path, path+".1"))
	appendTo(t, path, "after the rotation\n")
	eventually(t, out, "after the rotation\n")
	stop()

	assert.Equal(t, "before the rotation\nafter the rotation\n", out.String())
}

func TestTailCopiesAFileThatHadBeenReplacedBeforeItStarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongod.log")
	appendTo(t, path, "old run\n")
	info, err := os.Stat(path)
	require.NoError(t, err)
	// the process renamed the old log and wrote a new one before the tail looked at the file
	require.NoError(t, os.Rename(path, path+".1"))
	appendTo(t, path, "new run\n")
	out := &syncBuffer{}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		tailLog(anyFiles{}, path, info, out, done, testTailPoll)
		close(finished)
	}()

	eventually(t, out, "new run\n")
	close(done)
	<-finished

	assert.Equal(t, "new run\n", out.String())
}

func TestTailWaitsForTheEndOfALine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongod.log")
	out, stop := startTail(t, path)

	appendTo(t, path, "half a ")
	time.Sleep(10 * testTailPoll)
	assert.Equal(t, "", out.String())
	appendTo(t, path, "line\n")
	eventually(t, out, "half a line\n")
	stop()

	assert.Equal(t, "half a line\n", out.String())
}

func TestTailCopiesWhatIsLeftWhenItStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongod.log")
	out, stop := startTail(t, path)

	appendTo(t, path, "last words\nand an unfinished one")
	stop()

	assert.Equal(t, "last words\nand an unfinished one\n", out.String())
}

func TestLogPathOf(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		config string
		want   string
	}{
		"a file":                 {"systemLog:\n  destination: file\n  path: /var/log/mongod.log\n", "/var/log/mongod.log"},
		"a path alone is a file": {"systemLog:\n  path: /var/log/mongod.log\n", "/var/log/mongod.log"},
		"syslog":                 {"systemLog:\n  destination: syslog\n  path: /var/log/mongod.log\n", ""},
		"no path (stdout)":       {"systemLog:\n  verbosity: 1\n", ""},
		"no section":             {"net:\n  port: 27017\n", ""},
		"not yaml":               {"{{{ not yaml", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "case.conf")
			require.NoError(t, os.WriteFile(path, []byte(tc.config), 0o600))
			facts, err := onlyIn(dir).readConfig(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, facts.logPath)
		})
	}
	facts, err := onlyIn(dir).readConfig(filepath.Join(dir, "missing.conf"))
	require.NoError(t, err)
	assert.Equal(t, "", facts.logPath)
}

// A mongod that writes its log to the file of its config.
const loggingMongod = `#!/bin/sh
log=$(sed -n 's/^  path: //p' "$2")
mv "$log" "$log.1" 2>/dev/null
echo "line one" >> "$log"
echo "line two" >> "$log"
trap 'echo "line three" >> "$log"; exit 0' TERM
touch "$2.ready"
while true; do sleep 0.1; done
`

func TestSupervisorCopiesTheLogFileOfTheChildToItsOutput(t *testing.T) {
	f := newFixture(t)
	f.sup.logPoll = testTailPoll
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, BinaryMongod), []byte(loggingMongod), 0o755)) //nolint:gosec
	logPath := filepath.Join(f.dir, "mongod.log")
	appendTo(t, logPath, "an earlier run\n")
	cfg := filepath.Join(f.dir, "main.conf")
	require.NoError(t, os.WriteFile(cfg, []byte("systemLog:\n  destination: file\n  path: "+logPath+"\n"), 0o600))

	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain})
	require.NoError(t, err)
	f.waitReady(cfg)
	eventually(t, f.stdout, "line two\n")
	require.NoError(t, f.sup.Stop(t.Context(), RoleMain, 5*time.Second, false))
	waitDone(t, e)

	out := f.stdout.String()
	assert.Contains(t, out, "line one\nline two\n")
	assert.NotContains(t, out, "an earlier run")
	// the tail copies what is left after the process has exited, a moment later
	eventually(t, f.stdout, "line three\n")
}

func TestSupervisorDoesNotFollowTheLogWhenItIsSwitchedOff(t *testing.T) {
	f := newFixture(t)
	f.sup.tailLogs = false
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, BinaryMongod), []byte(loggingMongod), 0o755)) //nolint:gosec
	logPath := filepath.Join(f.dir, "mongod.log")
	cfg := filepath.Join(f.dir, "main.conf")
	require.NoError(t, os.WriteFile(cfg, []byte("systemLog:\n  destination: file\n  path: "+logPath+"\n"), 0o600))

	e, err := f.sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain})
	require.NoError(t, err)
	f.waitReady(cfg)
	require.NoError(t, f.sup.Stop(t.Context(), RoleMain, 5*time.Second, false))
	waitDone(t, e)

	assert.NotContains(t, f.stdout.String(), "line one")
}
