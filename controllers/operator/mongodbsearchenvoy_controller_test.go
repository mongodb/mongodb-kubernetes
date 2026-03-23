package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
)

func TestBuildReplicaSetRoute(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      string
		expectedSNI   string
		expectedProxy string
	}{
		{
			name:          "no endpoint uses proxy service FQDN",
			endpoint:      "",
			expectedSNI:   "mdb-search-search-lb-svc.test-ns.svc.cluster.local",
			expectedProxy: "mdb-search-search-lb-svc",
		},
		{
			name:          "endpoint with port uses endpoint hostname",
			endpoint:      "lb.example.com:443",
			expectedSNI:   "lb.example.com",
			expectedProxy: "mdb-search-search-lb-svc",
		},
		{
			name:          "endpoint without port uses endpoint as-is",
			endpoint:      "lb.example.com",
			expectedSNI:   "lb.example.com",
			expectedProxy: "mdb-search-search-lb-svc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mdb-search",
					Namespace: "test-ns",
				},
			}
			if tt.endpoint != "" {
				search.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Mode:     searchv1.LBModeManaged,
					Endpoint: tt.endpoint,
				}
			}

			route := buildReplicaSetRoute(search)

			assert.Equal(t, "rs", route.Name)
			assert.Equal(t, "rs", route.NameSafe)
			assert.Equal(t, tt.expectedSNI, route.SNIHostname)
			assert.Equal(t, tt.expectedProxy, route.ProxyServiceName)
			assert.Equal(t, "mdb-search-search-svc.test-ns.svc.cluster.local", route.UpstreamHost)
			assert.Equal(t, int32(27028), route.UpstreamPort)
		})
	}
}

func TestBuildShardRoutes(t *testing.T) {
	shardNames := []string{"mdb-sh-0", "mdb-sh-1"}

	tests := []struct {
		name         string
		endpoint     string
		expectedSNIs []string
	}{
		{
			name:     "no endpoint uses proxy service FQDNs",
			endpoint: "",
			expectedSNIs: []string{
				"mdb-search-search-0-mdb-sh-0-proxy-svc.test-ns.svc.cluster.local",
				"mdb-search-search-0-mdb-sh-1-proxy-svc.test-ns.svc.cluster.local",
			},
		},
		{
			name:     "endpoint template resolves per shard",
			endpoint: "mongot-{shardName}-ns.apps.example.com:443",
			expectedSNIs: []string{
				"mongot-mdb-sh-0-ns.apps.example.com",
				"mongot-mdb-sh-1-ns.apps.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := &searchv1.MongoDBSearch{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mdb-search",
					Namespace: "test-ns",
				},
			}
			if tt.endpoint != "" {
				search.Spec.LoadBalancer = &searchv1.LoadBalancerConfig{
					Mode:     searchv1.LBModeManaged,
					Endpoint: tt.endpoint,
				}
			}

			routes := buildShardRoutes(search, shardNames)

			assert.Len(t, routes, 2)
			for i, route := range routes {
				assert.Equal(t, shardNames[i], route.Name)
				assert.Equal(t, tt.expectedSNIs[i], route.SNIHostname)
				expectedProxy := "mdb-search-search-0-" + shardNames[i] + "-proxy-svc"
				assert.Equal(t, expectedProxy, route.ProxyServiceName)
			}
		})
	}
}
