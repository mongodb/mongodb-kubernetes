package mongodbproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const maxBodyBytes = 1 << 20

// logIDPattern matches the request and operation identities that are safe to log and echo.
var logIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

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

// requestLog collects the identity and the outcome of one HTTP call. Every log line of the call carries it,
// so that a caller can correlate its request with the proxy's answer without the proxy logging request bodies.
type requestLog struct {
	http.ResponseWriter
	started     time.Time
	requestID   string
	operationID string
	role        Role
	status      int
	errorClass  string
}

func (h *handler) request(w http.ResponseWriter, r *http.Request) *requestLog {
	id := r.Header.Get("X-Request-Id")
	if !logIDPattern.MatchString(id) {
		id = uuid.NewString()
	}
	w.Header().Set("X-Request-Id", id)
	return &requestLog{ResponseWriter: w, started: time.Now(), requestID: id, status: http.StatusOK}
}

func (r *requestLog) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// logOperationID keeps an identity that a caller supplied out of the log when it is not a safe token.
func logOperationID(id string) string {
	if id == "" || logIDPattern.MatchString(id) {
		return id
	}
	return "[redacted]"
}

func (r *requestLog) fields() []any {
	role := r.role
	if role != RoleMain && role != RoleEphemeral {
		role = ""
	}
	return []any{
		"request_id", r.requestID, "operation_id", logOperationID(r.operationID),
		"role", role, "http_status", r.status, "error_class", r.errorClass,
		"duration_ms", float64(time.Since(r.started).Microseconds()) / 1000,
	}
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	call := h.request(w, r)
	status := h.s.Status()
	call.role = status.Role
	writeJSON(call, http.StatusOK, status)
	h.log.Debugw("status requested", append(call.fields(), "state", status.State)...)
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	call := h.request(w, r)
	var req StartRequest
	// The deferred line also covers a request that fails before the process is spawned.
	defer func() {
		h.log.Infow("start requested", append(call.fields(), "waitForExit", req.WaitForExit)...)
	}()
	if err := decode(call, r, &req); err != nil {
		h.fail(call, err)
		return
	}
	call.role, call.operationID = req.Role, req.OperationID
	e, err := h.s.Start(req)
	if err != nil {
		h.fail(call, err)
		return
	}

	if req.WaitForExit {
		// If the client goes away we stop waiting but leave the child alone.
		select {
		case <-e.Done():
		case <-r.Context().Done():
			h.fail(call, r.Context().Err())
			return
		}
	}
	writeJSON(call, http.StatusOK, h.s.StatusOf(e))
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	call := h.request(w, r)
	var req StopRequest
	var timeout time.Duration
	defer func() {
		fields := append(call.fields(), "timeout", timeout.String(), "force", req.Force)
		h.log.Infow("stop requested", fields...)
	}()
	if err := decode(call, r, &req); err != nil {
		h.fail(call, err)
		return
	}
	call.role, call.operationID = req.Role, req.OperationID
	if err := req.validate(); err != nil {
		h.fail(call, err)
		return
	}
	timeout = time.Duration(req.TimeoutSeconds * float64(time.Second))
	if err := h.s.Stop(r.Context(), req.Role, timeout, req.Force); err != nil {
		h.fail(call, err)
		return
	}
	h.log.Infow("process stopped", append(call.fields(), "force", req.Force)...)
	writeJSON(call, http.StatusOK, h.s.Status())
}

func (h *handler) fail(w *requestLog, err error) {
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
	w.errorClass, w.status = code, status
	h.log.Warnw("request failed", w.fields()...)
	writeJSON(w, status, errorResponse{Code: code, Error: err.Error()})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
