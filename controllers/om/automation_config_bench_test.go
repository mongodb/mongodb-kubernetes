package om

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/api"
)

// acFakeTransport serves one automation config payload from memory and counts
// the GET requests. It removes TCP and server-goroutine noise from the
// benchmarks, which measure the client-side allocation cost.
type acFakeTransport struct {
	mu      sync.Mutex
	payload []byte
	gets    int
}

func (t *acFakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.gets++
	t.mu.Unlock()

	status := http.StatusOK
	body := t.payload
	if req.Method == http.MethodPut {
		body = nil
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Request:       req,
		Header:        http.Header{"X-Mongodb-Service-Version": []string{"versionString=8.0.0"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}, nil
}

func (t *acFakeTransport) getCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gets
}

// buildAutomationConfigPayload returns an automation config shaped JSON payload
// of at least size bytes.
func buildAutomationConfigPayload(size int) []byte {
	process := `{"name":"rs-%d","hostname":"rs-%d.svc.cluster.local","version":"7.0.0","authSchemaVersion":5,` +
		`"systemLog":{"destination":"file","path":"/var/log/mongodb/mongod.log","logAppend":true},` +
		`"logRotate":{"sizeThresholdMB":1000,"timeThresholdHrs":24},` +
		`"processType":"mongod","disabled":false,"args26":{"net":{"port":27017,"bindIpAll":true},` +
		`"storage":{"dbPath":"/data/db"},"setParameter":{"enableTestCommands":"false"}},` +
		`"lastGoalVersionAchieved":5,"plan":[]}`

	var processes strings.Builder
	written := 0
	for i := 0; written < size; i++ {
		if i > 0 {
			processes.WriteString(",")
		}
		n, _ := fmt.Fprintf(&processes, process, i, i)
		written += n
	}

	var sb strings.Builder
	sb.WriteString(`{"version":123,"auth":{"autoAuthMechanism":"SCRAM-SHA-256","usersWanted":[],"key":"`)
	sb.WriteString(strings.Repeat("k", 64))
	sb.WriteString(`"},"tls":{"clientCertificateMode":"OPTIONAL"},"processes":[`)
	sb.WriteString(processes.String())
	sb.WriteString(`],"replicaSets":[{"_id":"rs","members":[{"_id":0,"host":"rs-0.svc:27017"}]}]}`)
	return []byte(sb.String())
}

func (t *acFakeTransport) connection() *HTTPOmConnection {
	return NewOpsManagerConnectionWithOptions(
		&OMContext{BaseURL: "http://ops-manager.test", GroupID: "1"},
		func(c *api.Client) error {
			c.HTTPClient.Transport = t
			return nil
		},
	).(*HTTPOmConnection)
}

// BenchmarkAutomationConfigReadSequence measures the automation config reads of
// one reconcile on one connection per iteration. The steady-state sequence is
// what a replica set reconcile issues when nothing changes: ReadDeployment,
// ReadAutomationConfig, GetAgentAuthMode, ReadDeployment. The read-modify-write
// sequence adds a no-op ReadUpdateDeployment.
func BenchmarkAutomationConfigReadSequence(b *testing.B) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	defer restore()

	sequences := []struct {
		name  string
		reads func(ctx context.Context, conn *HTTPOmConnection) error
	}{
		{
			name: "steady-state",
			reads: func(ctx context.Context, conn *HTTPOmConnection) error {
				if _, err := conn.ReadDeployment(ctx); err != nil {
					return err
				}
				if _, err := conn.ReadAutomationConfig(ctx); err != nil {
					return err
				}
				if _, err := conn.GetAgentAuthMode(ctx); err != nil {
					return err
				}
				_, err := conn.ReadDeployment(ctx)
				return err
			},
		},
		{
			name: "with-read-modify-write",
			reads: func(ctx context.Context, conn *HTTPOmConnection) error {
				if _, err := conn.ReadDeployment(ctx); err != nil {
					return err
				}
				if _, err := conn.ReadAutomationConfig(ctx); err != nil {
					return err
				}
				if _, err := conn.GetAgentAuthMode(ctx); err != nil {
					return err
				}
				return conn.ReadUpdateDeployment(ctx, func(Deployment) error { return nil }, nopLogger())
			},
		},
	}

	for _, sequence := range sequences {
		for _, size := range []int{64 << 10, 978 << 10, 7 << 20} {
			b.Run(fmt.Sprintf("%s/payload=%dKiB", sequence.name, size>>10), func(b *testing.B) {
				transport := &acFakeTransport{payload: buildAutomationConfigPayload(size)}
				ctx := b.Context()

				b.ReportAllocs()
				b.SetBytes(int64(len(transport.payload)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := sequence.reads(ctx, transport.connection()); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(transport.getCount())/float64(b.N), "ac-gets/op")
			})
		}
	}
}

// BenchmarkAutomationConfigReads measures one read on a cold connection against
// one read on a connection that already read the automation config. The
// difference is what reuse saves per redundant read.
func BenchmarkAutomationConfigReads(b *testing.B) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	defer restore()

	transport := &acFakeTransport{payload: buildAutomationConfigPayload(7 << 20)}
	ctx := b.Context()

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			conn := transport.connection()
			if _, err := conn.ReadDeployment(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("warm", func(b *testing.B) {
		b.ReportAllocs()
		conn := transport.connection()
		if _, err := conn.ReadDeployment(ctx); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := conn.ReadDeployment(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("cold_after_write", func(b *testing.B) {
		b.ReportAllocs()
		conn := transport.connection()
		for i := 0; i < b.N; i++ {
			if _, err := conn.ReadDeployment(ctx); err != nil {
				b.Fatal(err)
			}
			if _, err := conn.UpdateDeployment(ctx, Deployment{"version": i}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
