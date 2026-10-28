package om

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

const testAutomationConfigPath = "/api/public/v1.0/groups/1/automationConfig"

// acRequestCounter is a fake Ops Manager automation config endpoint that counts
// the requests it receives, by method.
type acRequestCounter struct {
	mu          sync.Mutex
	gets        int
	puts        int
	otherWrites int
	failNext    bool
	payload     []byte
}

// setVersion rewrites the payload the fake endpoint serves, as another writer would.
func (c *acRequestCounter) setVersion(t *testing.T, version int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(c.payload, &payload))
	payload["version"] = version
	updated, err := json.Marshal(payload)
	require.NoError(t, err)
	c.payload = updated
}

func (c *acRequestCounter) counts() (gets, puts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.puts
}

func (c *acRequestCounter) unrelatedWrites() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.otherWrites
}

func (c *acRequestCounter) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(testAutomationConfigPath, func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			c.gets++
			if c.failNext {
				c.failNext = false
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"detail":"forced failure"}`))
				return
			}
			_, _ = w.Write(c.payload)
		case http.MethodPut:
			c.puts++
			_, _ = w.Write(c.payload)
		}
	})
	mux.HandleFunc(testAutomationConfigPath+"/auditLogRotateConfig", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.puts++
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/public/v1.0/groups/1", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.otherWrites++
		_, _ = w.Write(c.payload)
	})
	return mux
}

func newACRequestCounter(t *testing.T) (*acRequestCounter, *HTTPOmConnection) {
	t.Helper()
	counter := &acRequestCounter{payload: acTestPayload()}
	srv := httptest.NewServer(counter.handler())
	t.Cleanup(srv.Close)
	return counter, NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"}).(*HTTPOmConnection)
}

func acTestPayload() []byte {
	payload, err := json.Marshal(map[string]any{
		"version": 7,
		"auth": map[string]any{
			"autoAuthMechanism": "SCRAM-SHA-256",
			"usersWanted":       []any{map[string]any{"user": "mms-automation"}},
		},
		"processes": []any{map[string]any{
			"name":     "rs-0",
			"hostname": "rs-0.svc",
			"version":  "7.0.0",
		}},
		"replicaSets": []any{map[string]any{"_id": "rs", "members": []any{map[string]any{"_id": 0, "host": "rs-0.svc:27017"}}}},
	})
	if err != nil {
		panic(err)
	}
	return payload
}

func nopLogger() *zap.SugaredLogger { return zap.NewNop().Sugar() }

// TestAutomationConfigReadsShareOneGet verifies that the read methods reuse one
// automation config response per connection.
func TestAutomationConfigReadsShareOneGet(t *testing.T) {
	ctx := t.Context()
	counter, conn := newACRequestCounter(t)

	_, err := conn.ReadDeployment(ctx)
	require.NoError(t, err)
	_, err = conn.ReadAutomationConfig(ctx)
	require.NoError(t, err)
	mode, err := conn.GetAgentAuthMode(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SCRAM-SHA-256", mode)
	_, err = conn.ReadDeployment(ctx)
	require.NoError(t, err)

	gets, _ := counter.counts()
	assert.Equal(t, 1, gets, "four automation config reads on one connection should issue one GET")
}

// TestAutomationConfigReadUpdateReadsFreshState verifies that a read-update reads
// from Ops Manager rather than from the cache, so a write made through another
// connection is not lost.
func TestAutomationConfigReadUpdateReadsFreshState(t *testing.T) {
	ctx := t.Context()
	counter, conn := newACRequestCounter(t)

	_, err := conn.ReadDeployment(ctx)
	require.NoError(t, err)

	// Another operator connection (or a user in the Ops Manager UI) changes the
	// automation config after this connection cached it.
	counter.setVersion(t, 8)

	var updated Deployment
	var putVersion any
	require.NoError(t, conn.ReadUpdateDeployment(ctx, func(d Deployment) error {
		putVersion = d["version"]
		d["touched"] = true
		updated = d
		return nil
	}, nopLogger()))
	assert.Equal(t, float64(8), putVersion, "the read of a read-update must observe the latest state")
	require.NotNil(t, updated)

	gets, puts := counter.counts()
	assert.Equal(t, 2, gets, "the read of a read-update must not reuse the cached body")
	assert.Equal(t, 1, puts)
}

// TestAutomationConfigReadUpdateReusesCache verifies that a read-update that
// changes nothing does not refetch, and that a change invalidates the cache.
func TestAutomationConfigReadUpdateReusesCache(t *testing.T) {
	ctx := t.Context()
	counter, conn := newACRequestCounter(t)

	_, err := conn.ReadDeployment(ctx)
	require.NoError(t, err)

	require.NoError(t, conn.ReadUpdateDeployment(ctx, func(Deployment) error { return nil }, nopLogger()))
	gets, puts := counter.counts()
	assert.Equal(t, 2, gets, "the read of a read-update goes to Ops Manager")
	assert.Equal(t, 0, puts)

	require.NoError(t, conn.ReadUpdateDeployment(ctx, func(d Deployment) error {
		d["version"] = 8
		return nil
	}, nopLogger()))
	gets, puts = counter.counts()
	assert.Equal(t, 3, gets)
	assert.Equal(t, 1, puts)

	_, err = conn.ReadDeployment(ctx)
	require.NoError(t, err)
	gets, _ = counter.counts()
	assert.Equal(t, 4, gets, "a read after an automation config write must fetch again")
}

// TestAutomationConfigWriteInvalidatesCache verifies that UpdateDeployment, and a
// write to an automation config sub-resource, invalidate the cached body.
func TestAutomationConfigWriteInvalidatesCache(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(ctx context.Context, conn *HTTPOmConnection) error
	}{
		{
			name: "UpdateDeployment",
			write: func(ctx context.Context, conn *HTTPOmConnection) error {
				_, err := conn.UpdateDeployment(ctx, Deployment{"version": 8})
				return err
			},
		},
		{
			name: "audit log rotation",
			write: func(ctx context.Context, conn *HTTPOmConnection) error {
				_, err := conn.UpdateAuditLogRotation(ctx, automationconfig.AcLogRotate{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			counter, conn := newACRequestCounter(t)

			_, err := conn.ReadDeployment(ctx)
			require.NoError(t, err)
			require.NoError(t, tc.write(ctx, conn))

			_, err = conn.ReadDeployment(ctx)
			require.NoError(t, err)
			gets, _ := counter.counts()
			assert.Equal(t, 2, gets)
		})
	}
}

// TestAutomationConfigCacheIgnoresUnrelatedWrites verifies that a write outside
// the automation config subtree keeps the cached body.
func TestAutomationConfigCacheIgnoresUnrelatedWrites(t *testing.T) {
	ctx := t.Context()
	counter, conn := newACRequestCounter(t)

	_, err := conn.ReadDeployment(ctx)
	require.NoError(t, err)

	// Project updates go to the orgs/groups path, not the automation config.
	_, err = conn.UpdateProject(ctx, &Project{ID: "1", Name: "my-project", OrgID: "org"})
	require.NoError(t, err)
	require.Equal(t, 1, counter.unrelatedWrites())
	_, err = conn.ReadDeployment(ctx)
	require.NoError(t, err)

	gets, _ := counter.counts()
	assert.Equal(t, 1, gets)
}

// TestAutomationConfigReadErrorIsNotCached verifies that a failed read is not
// stored and the next read retries the request.
func TestAutomationConfigReadErrorIsNotCached(t *testing.T) {
	ctx := t.Context()
	counter := &acRequestCounter{payload: acTestPayload(), failNext: true}
	srv := httptest.NewServer(counter.handler())
	t.Cleanup(srv.Close)
	conn := NewOpsManagerConnectionWithOptions(&OMContext{BaseURL: srv.URL, GroupID: "1"}, OptionRetryConfig(0, 0, 1))

	_, err := conn.ReadDeployment(ctx)
	require.Error(t, err)

	_, err = conn.ReadDeployment(ctx)
	require.NoError(t, err)

	gets, _ := counter.counts()
	assert.Equal(t, 2, gets)
}

// TestAutomationConfigCacheIsPerConnection verifies that two connections do not
// share a cached body.
func TestAutomationConfigCacheIsPerConnection(t *testing.T) {
	ctx := t.Context()
	counter, conn := newACRequestCounter(t)

	_, err := conn.ReadDeployment(ctx)
	require.NoError(t, err)

	other := NewOpsManagerConnection(&OMContext{BaseURL: conn.BaseURL(), GroupID: "1"})
	_, err = other.ReadDeployment(ctx)
	require.NoError(t, err)

	gets, _ := counter.counts()
	assert.Equal(t, 2, gets)
}
