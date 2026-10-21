package mongodbproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"
)

const maxBodyBytes = 1 << 20

type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// GET  /v1/process
// POST /v1/process/start
// POST /v1/process/stop
func NewHandler(s *Supervisor, log *zap.SugaredLogger) http.Handler {
	h := &handler{s: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/process", h.get)
	mux.HandleFunc("POST /v1/process/start", h.start)
	mux.HandleFunc("POST /v1/process/stop", h.stop)
	return mux
}

type handler struct {
	s   *Supervisor
	log *zap.SugaredLogger
}

func (h *handler) get(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.s.Status())
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if !decode(w, r, &req) {
		return
	}
	e, err := h.s.Start(req)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.log.Infow("process started", "role", req.Role, "configPath", req.ConfigPath, "waitForExit", req.WaitForExit)

	if req.WaitForExit {
		// If the client goes away we stop waiting but leave the child alone.
		select {
		case <-e.Done():
		case <-r.Context().Done():
			return
		}
	}
	writeJSON(w, http.StatusOK, h.s.StatusOf(e))
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	var req StopRequest
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		h.fail(w, err)
		return
	}
	timeout := time.Duration(req.TimeoutSeconds * float64(time.Second))
	if err := h.s.Stop(r.Context(), req.Role, timeout, req.Force); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Infow("process stopped", "role", req.Role, "force", req.Force)
	writeJSON(w, http.StatusOK, h.s.Status())
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, ErrRoleConflict):
		status, code = http.StatusConflict, "role_conflict"
	case errors.Is(err, ErrRoleMismatch):
		status, code = http.StatusConflict, "role_mismatch"
	case errors.Is(err, ErrShuttingDown):
		status, code = http.StatusServiceUnavailable, "shutting_down"
	case errors.Is(err, ErrStopTimeout):
		status, code = http.StatusGatewayTimeout, "stop_timeout"
	case errors.Is(err, ErrSpawn):
		status, code = http.StatusInternalServerError, "spawn_failed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusRequestTimeout, "canceled"
	}
	h.log.Warnw("request failed", "code", code, "error", err)
	writeJSON(w, status, errorResponse{Code: code, Error: err.Error()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Code: "invalid_request", Error: err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
