package om

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// BenchmarkAuthLookupComparison uses the same HTTP response for the master
// full-config lookup and the auth-only lookup. It includes localhost HTTP costs.
func BenchmarkAuthLookupComparison(b *testing.B) {
	undo := zap.ReplaceGlobals(zap.NewNop())
	b.Cleanup(undo)
	users := make([]*MongoDBUser, 1000)
	for i := range users {
		users[i] = &MongoDBUser{
			Username: fmt.Sprintf("user-%04d", i), Database: "admin",
			Mechanisms: []string{"SCRAM-SHA-256"}, Roles: []*Role{{Role: "readWrite", Database: "app"}},
			ScramSha256Creds: &ScramShaCreds{
				IterationCount: 15000, Salt: strings.Repeat("s", 24),
				ServerKey: strings.Repeat("k", 44), StoredKey: strings.Repeat("x", 44),
			},
		}
	}
	payload, err := json.Marshal(map[string]any{"auth": &Auth{AutoAuthMechanism: "SCRAM-SHA-256", Users: users}})
	require.NoError(b, err)
	b.Logf("payload bytes: %d", len(payload))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	b.Cleanup(srv.Close)
	for _, approach := range []string{"MasterFullConfig", "AuthOnly"} {
		b.Run(approach, func(b *testing.B) {
			connection := NewOpsManagerConnection(&OMContext{BaseURL: srv.URL, GroupID: "1"})
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for b.Loop() {
				if approach == "MasterFullConfig" {
					ac, err := connection.ReadAutomationConfig(b.Context())
					require.NoError(b, err)
					require.Equal(b, "SCRAM-SHA-256", ac.Auth.AutoAuthMechanism)
				} else {
					mode, err := connection.GetAgentAuthMode(b.Context())
					require.NoError(b, err)
					require.Equal(b, "SCRAM-SHA-256", mode)
				}
			}
		})
	}
}
