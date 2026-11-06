package mongodbproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	sup := NewSupervisor(WithEnv(testEnv), WithBinDir(dir), WithOutput(stdout, stdout), WithLogger(zap.New(core).Sugar()))
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

func TestLogsStartRequestCarriesTheCallerIdentity(t *testing.T) {
	f, logs := observedFixture(t)
	config := f.config("main", "run")
	body := `{"role":"main","operationId":"main-1","configPath":"` + config + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/process/start", strings.NewReader(body))
	req.Header.Set("X-Request-Id", "start-1")
	response := httptest.NewRecorder()

	NewHandler(f.sup, f.sup.log).ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "start-1", response.Header().Get("X-Request-Id"))
	f.waitReady(config)

	events := logs.FilterMessage("start requested").All()
	require.Len(t, events, 1)
	fields := events[0].ContextMap()
	assert.Equal(t, "main-1", fields["operation_id"])
	assert.Equal(t, "start-1", fields["request_id"])
	assert.EqualValues(t, http.StatusOK, fields["http_status"])
	assert.Equal(t, "", fields["error_class"])
	assert.NotContains(t, fields, "configPath")
}

func TestLogsRequestCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, header, event string
		status                          int
		errorClass                      string
		operationID                     string
	}{
		{"stop", "/v1/process/stop", `{"role":"main","timeoutSeconds":1}`, "agent-call-1", "stop requested", 200, "", ""},
		{"generated", "/v1/process/stop", `{"role":"main","timeoutSeconds":1}`, "", "stop requested", 200, "", ""},
		{"unsafe header", "/v1/process/stop", `{"role":"main","timeoutSeconds":1}`, "sensitive value", "stop requested", 200, "", ""},
		{"long header", "/v1/process/stop", `{"role":"main","timeoutSeconds":1}`, strings.Repeat("a", 129), "stop requested", 200, "", ""},
		{"invalid body", "/v1/process/start", `{"credential":"secret"}`, "agent-call-2", "start requested", 400, "invalid_request", ""},
		{"invalid role", "/v1/process/stop", `{"role":"secret","timeoutSeconds":1}`, "agent-call-3", "stop requested", 400, "invalid_request", ""},
		{"unsafe operation", "/v1/process/stop", `{"role":"main","operationId":"secret value","timeoutSeconds":1}`, "agent-call-4", "stop requested", 200, "", "[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, logs := observedFixture(t)
			handler := NewHandler(f.sup, f.sup.log)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Request-Id", tc.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			require.Equal(t, tc.status, response.Code)
			id := response.Header().Get("X-Request-Id")
			if logIDPattern.MatchString(tc.header) {
				assert.Equal(t, tc.header, id)
			} else {
				_, err := uuid.Parse(id)
				require.NoError(t, err)
			}
			events := logs.FilterMessage(tc.event).All()
			require.Len(t, events, 1)
			fields := events[0].ContextMap()
			assert.Equal(t, id, fields["request_id"])
			assert.EqualValues(t, tc.status, fields["http_status"])
			assert.Equal(t, tc.errorClass, fields["error_class"])
			assert.Equal(t, tc.operationID, fields["operation_id"])
			assert.GreaterOrEqual(t, fields["duration_ms"], float64(0))
			assert.NotContains(t, fields, "configPath")
			for _, event := range logs.All() {
				assert.NotContains(t, event.ContextMap(), "error")
				assert.NotContains(t, event.ContextMap(), "credential")
				assert.NotContains(t, event.ContextMap(), "body")
			}
			if tc.errorClass != "" {
				failure := logs.FilterMessage("request failed").All()
				require.Len(t, failure, 1)
				assert.EqualValues(t, tc.status, failure[0].ContextMap()["http_status"])
				assert.Equal(t, id, failure[0].ContextMap()["request_id"])
			}
		})
	}
}

func TestLogsCanceledStartOutcome(t *testing.T) {
	f, logs := observedFixture(t)
	config := f.config("ephemeral", "run")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := `{"role":"ephemeral","operationId":"job-1","waitForExit":true,"configPath":"` + config + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/process/start", strings.NewReader(body)).WithContext(ctx)
	response := httptest.NewRecorder()

	NewHandler(f.sup, f.sup.log).ServeHTTP(response, req)
	assert.Equal(t, http.StatusRequestTimeout, response.Code)
	failed := logs.FilterMessage("request failed").All()
	require.Len(t, failed, 1)
	assert.Equal(t, "canceled", failed[0].ContextMap()["error_class"])
	assert.EqualValues(t, http.StatusRequestTimeout, failed[0].ContextMap()["http_status"])
}
