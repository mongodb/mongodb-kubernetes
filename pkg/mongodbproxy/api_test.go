package mongodbproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newServer(t *testing.T) (*fixture, *httptest.Server) {
	t.Helper()
	f := newFixture(t)
	srv := httptest.NewServer(NewHandler(f.sup, zap.NewNop().Sugar()))
	t.Cleanup(srv.Close)
	return f, srv
}

func post(t *testing.T, url string, body any, out any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw)) //nolint:gosec,noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

func getStatus(t *testing.T, srv *httptest.Server) Status {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/process") //nolint:gosec,noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var st Status
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&st))
	return st
}

func TestAPIStartGetStop(t *testing.T) {
	f, srv := newServer(t)
	assert.Equal(t, Status{State: StateIdle}, getStatus(t, srv))

	var st Status
	cfg := f.config("main", "run")
	code := post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: cfg, Role: RoleMain}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateRunning, st.State)
	f.waitReady(cfg)
	running := getStatus(t, srv)
	assert.Equal(t, StateRunning, running.State)
	assert.Equal(t, RoleMain, running.Role)
	assert.Nil(t, running.ExitCode)

	require.NotNil(t, st.PID)
	pid := *st.PID
	require.NotNil(t, running.LaunchEnvironment)
	st = Status{}
	code = post(t, srv.URL+"/v1/process/stop", StopRequest{PID: pid, TimeoutSeconds: 5, Role: RoleMain}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateExited, st.State)
	require.NotNil(t, st.PID)
	assert.Equal(t, pid, *st.PID)
	exited := getStatus(t, srv)
	assert.Equal(t, StateExited, exited.State)
	require.NotNil(t, exited.PID)
	assert.Equal(t, pid, *exited.PID)
	assert.Nil(t, exited.LaunchEnvironment)
}

func TestAPIStartWaitForExitReturnsExitStatus(t *testing.T) {
	f, srv := newServer(t)

	var st Status
	code := post(t, srv.URL+"/v1/process/start",
		StartRequest{ConfigPath: f.config("eph", "exit:7"), Role: RoleEphemeral, WaitForExit: true}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateExited, st.State)
	require.NotNil(t, st.PID)
	assert.Positive(t, *st.PID)
	assert.Nil(t, st.LaunchEnvironment)
	require.NotNil(t, st.ExitCode)
	assert.Equal(t, 7, *st.ExitCode)
}

func TestAPIGetAndStopAnswerWhileStartWaits(t *testing.T) {
	f, srv := newServer(t)

	cfg := f.config("eph", "run")
	done := make(chan Status, 1)
	go func() {
		var st Status
		post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: cfg, Role: RoleEphemeral, WaitForExit: true}, &st)
		done <- st
	}()

	f.waitReady(cfg)
	running := getStatus(t, srv)
	assert.Equal(t, StateRunning, running.State)
	require.NotNil(t, running.PID)

	var st Status
	assert.Equal(t, http.StatusOK, post(t, srv.URL+"/v1/process/stop", StopRequest{PID: *running.PID, TimeoutSeconds: 5, Role: RoleEphemeral}, &st))

	select {
	case waited := <-done:
		assert.Equal(t, StateExited, waited.State)
	case <-time.After(5 * time.Second):
		t.Fatal("start with waitForExit did not return after stop")
	}
}

func TestAPIErrors(t *testing.T) {
	f, srv := newServer(t)
	main := f.config("main", "run")

	tests := map[string]struct {
		path   string
		body   any
		status int
		code   string
	}{
		"start without role":        {"/v1/process/start", StartRequest{ConfigPath: main}, http.StatusBadRequest, "invalid_request"},
		"start with unknown json":   {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "shell": "rm -rf /"}, http.StatusBadRequest, "invalid_request"},
		"start with a reserved arg": {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "other": map[string]any{"args": []string{"--fork"}}}, http.StatusBadRequest, "invalid_request"},
		"start with env as object":  {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "other": map[string]any{"env": map[string]string{"LD_PRELOAD": "/x"}}}, http.StatusBadRequest, "invalid_request"},
		"stop without timeout":      {"/v1/process/stop", StopRequest{PID: 1, Role: RoleMain}, http.StatusBadRequest, "invalid_request"},
		"stop without pid":          {"/v1/process/stop", StopRequest{TimeoutSeconds: 1, Role: RoleMain}, http.StatusBadRequest, "invalid_request"},
		"force stop without pid":    {"/v1/process/stop", StopRequest{Force: true, Role: RoleMain}, http.StatusBadRequest, "invalid_request"},
		"stop with negative pid":    {"/v1/process/stop", StopRequest{PID: -1, Force: true, Role: RoleMain}, http.StatusBadRequest, "invalid_request"},
		"stop with bad role":        {"/v1/process/stop", StopRequest{PID: 1, TimeoutSeconds: 1, Role: "other"}, http.StatusBadRequest, "invalid_request"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var e errorResponse
			assert.Equal(t, tc.status, post(t, srv.URL+tc.path, tc.body, &e))
			assert.Equal(t, tc.code, e.Code)
		})
	}

	t.Run("role conflict and mismatch", func(t *testing.T) {
		require.Equal(t, http.StatusOK, post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: main, Role: RoleMain}, nil))
		f.waitReady(main)

		var e errorResponse
		assert.Equal(t, http.StatusConflict, post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: main, Role: RoleEphemeral}, &e))
		assert.Equal(t, "role_conflict", e.Code)

		pid := getStatus(t, srv).PID
		require.NotNil(t, pid)
		assert.Equal(t, http.StatusConflict, post(t, srv.URL+"/v1/process/stop", StopRequest{PID: *pid, TimeoutSeconds: 1, Role: RoleEphemeral}, &e))
		assert.Equal(t, "role_mismatch", e.Code)
	})

	t.Run("stop timeout", func(t *testing.T) {
		require.NoError(t, stopCurrent(f.sup, t.Context(), "", 0, true))
		stubborn := f.config("stubborn", "ignoreterm")
		require.Equal(t, http.StatusOK, post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: stubborn, Role: RoleMain}, nil))
		f.waitReady(stubborn)

		var e errorResponse
		pid := getStatus(t, srv).PID
		require.NotNil(t, pid)
		assert.Equal(t, http.StatusGatewayTimeout, post(t, srv.URL+"/v1/process/stop", StopRequest{PID: *pid, TimeoutSeconds: 0.3, Role: RoleMain}, &e))
		assert.Equal(t, "stop_timeout", e.Code)
	})
}

func TestAPIStopDoesNotSignalReplacementProcess(t *testing.T) {
	f, srv := newServer(t)
	first := f.start(RoleMain, "exit:0")
	waitDone(t, first)
	old := getStatus(t, srv)
	second := f.start(RoleMain, "run")
	current := getStatus(t, srv)
	require.NotNil(t, old.PID)
	require.NotNil(t, current.PID)
	require.NotEqual(t, *current.PID, *old.PID)

	var e errorResponse
	code := post(t, srv.URL+"/v1/process/stop", StopRequest{PID: *old.PID, Role: RoleMain, Force: true}, &e)
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "pid_mismatch", e.Code)
	require.False(t, second.finished())
}

func TestAPIStartLogsRequestIDAndPID(t *testing.T) {
	f, logs := observedFixture(t)
	config := f.config("main", "run")
	body := `{"role":"main","configPath":"` + config + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/process/start", strings.NewReader(body))
	req.Header.Set("X-Request-Id", "start-1")
	response := httptest.NewRecorder()

	NewHandler(f.sup, f.sup.log).ServeHTTP(response, req)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "start-1", response.Header().Get("X-Request-Id"))
	f.waitReady(config)

	entries := logs.FilterMessage("start requested").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	assert.Equal(t, "start-1", fields["request_id"])
	pid := f.sup.Status().PID
	require.NotNil(t, pid)
	assert.EqualValues(t, *pid, fields["pid"])
	started := logs.FilterMessage("process started").All()
	require.Len(t, started, 1)
	assert.EqualValues(t, *pid, started[0].ContextMap()["pid"])
	assert.EqualValues(t, http.StatusOK, fields["http_status"])
	assert.Equal(t, "", fields["error_class"])
	assert.NotContains(t, fields, "configPath")
}

func TestAPILogsRequestCorrelation(t *testing.T) {
	const stopBody = `{"pid":1,"role":"main","timeoutSeconds":1}`
	for _, tc := range []struct {
		name, path, body, header, message string
		status                            int
		errorClass                        string
		newRequestID                      bool
	}{
		{"stop", "/v1/process/stop", stopBody, "agent-call-1", "stop requested", http.StatusOK, "", false},
		{"unsafe header", "/v1/process/stop", stopBody, "sensitive value", "stop requested", http.StatusOK, "", true},
		{"long header", "/v1/process/stop", stopBody, strings.Repeat("a", 129), "stop requested", http.StatusOK, "", true},
		{"invalid body", "/v1/process/start", `{"credential":"secret"}`, "agent-call-2", "start requested", http.StatusBadRequest, "invalid_request", false},
		{"invalid role", "/v1/process/stop", `{"pid":1,"role":"secret","timeoutSeconds":1}`, "agent-call-3", "stop requested", http.StatusBadRequest, "invalid_request", false},
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
			if tc.newRequestID {
				_, err := uuid.Parse(id)
				require.NoError(t, err)
			} else {
				assert.Equal(t, tc.header, id)
			}
			entries := logs.FilterMessage(tc.message).All()
			require.Len(t, entries, 1)
			fields := entries[0].ContextMap()
			assert.Equal(t, id, fields["request_id"])
			assert.EqualValues(t, tc.status, fields["http_status"])
			assert.Equal(t, tc.errorClass, fields["error_class"])
			assert.GreaterOrEqual(t, fields["duration_ms"], float64(0))
			assert.NotContains(t, fields, "configPath")
			for _, entry := range logs.All() {
				assert.NotContains(t, entry.ContextMap(), "error")
				assert.NotContains(t, entry.ContextMap(), "credential")
				assert.NotContains(t, entry.ContextMap(), "body")
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

func TestAPILogsCanceledStartOutcome(t *testing.T) {
	f, logs := observedFixture(t)
	config := f.config("ephemeral", "run")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	body := `{"role":"ephemeral","waitForExit":true,"configPath":"` + config + `"}`
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/process/start", strings.NewReader(body))
	response := httptest.NewRecorder()

	NewHandler(f.sup, f.sup.log).ServeHTTP(response, req)
	assert.Equal(t, http.StatusRequestTimeout, response.Code)
	failed := logs.FilterMessage("request failed").All()
	require.Len(t, failed, 1)
	assert.Equal(t, "canceled", failed[0].ContextMap()["error_class"])
	assert.EqualValues(t, http.StatusRequestTimeout, failed[0].ContextMap()["http_status"])
}
