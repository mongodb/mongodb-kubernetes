package mongodbproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	assert.Equal(t, StateIdle, getStatus(t, srv).State)

	var st Status
	cfg := f.config("main", "run")
	code := post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: cfg, Role: RoleMain}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateRunning, st.State)
	f.waitReady(cfg)
	assert.Equal(t, StateRunning, getStatus(t, srv).State)

	code = post(t, srv.URL+"/v1/process/stop", StopRequest{TimeoutSeconds: 5, Role: RoleMain}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateExited, st.State)
	assert.Equal(t, StateExited, getStatus(t, srv).State)
}

func TestAPIStartWaitForExitReturnsExitStatus(t *testing.T) {
	f, srv := newServer(t)

	var st Status
	code := post(t, srv.URL+"/v1/process/start",
		StartRequest{ConfigPath: f.config("eph", "exit:7"), Role: RoleEphemeral, WaitForExit: true}, &st)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, StateExited, st.State)
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
	assert.Equal(t, StateRunning, getStatus(t, srv).State)

	var st Status
	assert.Equal(t, http.StatusOK, post(t, srv.URL+"/v1/process/stop", StopRequest{TimeoutSeconds: 5, Role: RoleEphemeral}, &st))

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
		"start without role":      {"/v1/process/start", StartRequest{ConfigPath: main}, http.StatusBadRequest, "invalid_request"},
		"start with unknown json": {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "shell": "rm -rf /"}, http.StatusBadRequest, "invalid_request"},
		"start with raw args":     {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "other": map[string]any{"args": []string{"--fork"}}}, http.StatusBadRequest, "invalid_request"},
		"start with raw env":      {"/v1/process/start", map[string]any{"configPath": main, "role": "main", "other": map[string]any{"env": map[string]string{"LD_PRELOAD": "/x"}}}, http.StatusBadRequest, "invalid_request"},
		"stop without timeout":    {"/v1/process/stop", StopRequest{}, http.StatusBadRequest, "invalid_request"},
		"stop with bad role":      {"/v1/process/stop", StopRequest{TimeoutSeconds: 1, Role: "other"}, http.StatusBadRequest, "invalid_request"},
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

		assert.Equal(t, http.StatusConflict, post(t, srv.URL+"/v1/process/stop", StopRequest{TimeoutSeconds: 1, Role: RoleEphemeral}, &e))
		assert.Equal(t, "role_mismatch", e.Code)
	})

	t.Run("stop timeout", func(t *testing.T) {
		require.NoError(t, f.sup.Stop(t.Context(), "", 0, true))
		stubborn := f.config("stubborn", "ignoreterm")
		require.Equal(t, http.StatusOK, post(t, srv.URL+"/v1/process/start", StartRequest{ConfigPath: stubborn, Role: RoleMain}, nil))
		f.waitReady(stubborn)

		var e errorResponse
		assert.Equal(t, http.StatusGatewayTimeout, post(t, srv.URL+"/v1/process/stop", StopRequest{TimeoutSeconds: 0.3}, &e))
		assert.Equal(t, "stop_timeout", e.Code)
	})
}
