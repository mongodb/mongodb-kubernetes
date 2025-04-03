package dns

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMultiClusterAppDBHostnamesForMonitoring(t *testing.T) {
	assert.Equal(t,
		[]string{
			"om-db-0-0-svc.ns.svc.cluster.local",
			"om-db-0-1-svc.ns.svc.cluster.local",
		},
		GetMultiClusterHostnamesForMonitoring("om-db", "ns", 0, 2),
	)
	assert.Equal(t,
		[]string{
			"om-db-1-0-svc.ns.svc.cluster.local",
			"om-db-1-1-svc.ns.svc.cluster.local",
			"om-db-1-2-svc.ns.svc.cluster.local",
		},
		GetMultiClusterHostnamesForMonitoring("om-db", "ns", 1, 3),
	)
	assert.Equal(t,
		[]string{},
		GetMultiClusterHostnamesForMonitoring("om-db", "ns", 1, 0),
	)
}
