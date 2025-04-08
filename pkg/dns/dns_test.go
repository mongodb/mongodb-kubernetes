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
