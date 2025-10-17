package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func TestGetMultiClusterProcessHostnames(t *testing.T) {
	assert.Equal(t,
		[]string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
		},
		GetMultiClusterProcessHostnames("om-db", "ns", 0, 2, "", nil),
	)
	assert.Equal(t,
		[]string{
			"om-db-0-0-svc.ns.svc.cluster-2.local",
			"om-db-0-1-svc.ns.svc.cluster-2.local",
		},
		GetMultiClusterProcessHostnames("om-db", "ns", 0, 2, "cluster-2.local", nil),
	)
	assert.Equal(t,
		[]string{
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-1-2-svc.ns.svc.cluster.local",
		},
		GetMultiClusterProcessHostnames("om-db", "ns", 1, 3, "", nil),
	)
	assert.Equal(t,
		[]string{},
		GetMultiClusterProcessHostnames("om-db", "ns", 1, 0, "", nil),
	)
	assert.Equal(t,
		[]string{
			"om-db-0-0.some.domain",
			"om-db-0-1.some.domain",
			"om-db-0-2.some.domain",
		},
		GetMultiClusterProcessHostnames("om-db", "ns", 0, 3, "", ptr.To("some.domain")),
	)
}

func TestGetDNSNames(t *testing.T) {
	tests := []struct {
		name              string
		statefulSetName   string
		service           string
		namespace         string
		clusterDomain     string
		replicas          int
		externalDomain    *string
		expectedHostnames []string
		expectedNames     []string
	}{
		{
			name:            "3-member replica set with default cluster domain",
			statefulSetName: "test-rs",
			service:         "test-rs-svc",
			namespace:       "default",
			clusterDomain:   "",
			replicas:        3,
			externalDomain:  nil,
			expectedHostnames: []string{
				"test-rs-0.test-rs-svc.default.svc.cluster.local",
				"test-rs-1.test-rs-svc.default.svc.cluster.local",
				"test-rs-2.test-rs-svc.default.svc.cluster.local",
			},
			expectedNames: []string{"test-rs-0", "test-rs-1", "test-rs-2"},
		},
		{
			name:            "Single member replica set",
			statefulSetName: "single-rs",
			service:         "single-rs-svc",
			namespace:       "production",
			clusterDomain:   "",
			replicas:        1,
			externalDomain:  nil,
			expectedHostnames: []string{
				"single-rs-0.single-rs-svc.production.svc.cluster.local",
			},
			expectedNames: []string{"single-rs-0"},
		},
		{
			name:            "Custom cluster domain",
			statefulSetName: "custom-rs",
			service:         "custom-rs-svc",
			namespace:       "test-namespace",
			clusterDomain:   "my-cluster.local",
			replicas:        2,
			externalDomain:  nil,
			expectedHostnames: []string{
				"custom-rs-0.custom-rs-svc.test-namespace.svc.my-cluster.local",
				"custom-rs-1.custom-rs-svc.test-namespace.svc.my-cluster.local",
			},
			expectedNames: []string{"custom-rs-0", "custom-rs-1"},
		},
		{
			name:            "Custom cluster domain with leading dot (preserved as-is)",
			statefulSetName: "dotted-rs",
			service:         "dotted-rs-svc",
			namespace:       "default",
			clusterDomain:   ".custom.local",
			replicas:        2,
			externalDomain:  nil,
			expectedHostnames: []string{
				"dotted-rs-0.dotted-rs-svc.default.svc..custom.local",
				"dotted-rs-1.dotted-rs-svc.default.svc..custom.local",
			},
			expectedNames: []string{"dotted-rs-0", "dotted-rs-1"},
		},
		{
			name:            "External domain override",
			statefulSetName: "external-rs",
			service:         "external-rs-svc",
			namespace:       "default",
			clusterDomain:   "",
			replicas:        3,
			externalDomain:  ptr.To("example.com"),
			expectedHostnames: []string{
				"external-rs-0.example.com",
				"external-rs-1.example.com",
				"external-rs-2.example.com",
			},
			expectedNames: []string{"external-rs-0", "external-rs-1", "external-rs-2"},
		},
		{
			name:              "Zero replicas returns empty slices",
			statefulSetName:   "empty-rs",
			service:           "empty-rs-svc",
			namespace:         "default",
			clusterDomain:     "",
			replicas:          0,
			externalDomain:    nil,
			expectedHostnames: []string{},
			expectedNames:     []string{},
		},
		{
			name:            "Large replica count",
			statefulSetName: "large-rs",
			service:         "large-rs-svc",
			namespace:       "default",
			clusterDomain:   "",
			replicas:        10,
			externalDomain:  nil,
			expectedHostnames: []string{
				"large-rs-0.large-rs-svc.default.svc.cluster.local",
				"large-rs-1.large-rs-svc.default.svc.cluster.local",
				"large-rs-2.large-rs-svc.default.svc.cluster.local",
				"large-rs-3.large-rs-svc.default.svc.cluster.local",
				"large-rs-4.large-rs-svc.default.svc.cluster.local",
				"large-rs-5.large-rs-svc.default.svc.cluster.local",
				"large-rs-6.large-rs-svc.default.svc.cluster.local",
				"large-rs-7.large-rs-svc.default.svc.cluster.local",
				"large-rs-8.large-rs-svc.default.svc.cluster.local",
				"large-rs-9.large-rs-svc.default.svc.cluster.local",
			},
			expectedNames: []string{
				"large-rs-0", "large-rs-1", "large-rs-2", "large-rs-3", "large-rs-4",
				"large-rs-5", "large-rs-6", "large-rs-7", "large-rs-8", "large-rs-9",
			},
		},
		{
			name:            "Different namespace with custom domain",
			statefulSetName: "prod-rs",
			service:         "prod-rs-svc",
			namespace:       "production",
			clusterDomain:   "prod-cluster.local",
			replicas:        2,
			externalDomain:  nil,
			expectedHostnames: []string{
				"prod-rs-0.prod-rs-svc.production.svc.prod-cluster.local",
				"prod-rs-1.prod-rs-svc.production.svc.prod-cluster.local",
			},
			expectedNames: []string{"prod-rs-0", "prod-rs-1"},
		},
		{
			name:            "External domain with custom cluster domain (external takes precedence)",
			statefulSetName: "override-rs",
			service:         "override-rs-svc",
			namespace:       "default",
			clusterDomain:   "cluster.local",
			replicas:        2,
			externalDomain:  ptr.To("external.example.com"),
			expectedHostnames: []string{
				"override-rs-0.external.example.com",
				"override-rs-1.external.example.com",
			},
			expectedNames: []string{"override-rs-0", "override-rs-1"},
		},
		{
			name:            "Empty service name",
			statefulSetName: "no-svc-rs",
			service:         "",
			namespace:       "default",
			clusterDomain:   "",
			replicas:        2,
			externalDomain:  nil,
			expectedHostnames: []string{
				"no-svc-rs-0..default.svc.cluster.local",
				"no-svc-rs-1..default.svc.cluster.local",
			},
			expectedNames: []string{"no-svc-rs-0", "no-svc-rs-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostnames, names := GetDNSNames(
				tt.statefulSetName,
				tt.service,
				tt.namespace,
				tt.clusterDomain,
				tt.replicas,
				tt.externalDomain,
			)

			assert.Equal(t, tt.expectedHostnames, hostnames, "Hostnames mismatch")
			assert.Equal(t, tt.expectedNames, names, "Names mismatch")
			assert.Equal(t, tt.replicas, len(hostnames), "Hostname count should match replicas")
			assert.Equal(t, tt.replicas, len(names), "Name count should match replicas")
		})
	}
}
