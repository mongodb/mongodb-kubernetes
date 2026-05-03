package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
)

// TestExternalSearchSource_HostSeeds_ReturnsTopLevelHostAndPorts is the
// regression guard for the MC routing strategy: the external source's seed
// list comes from spec.source.external.hostAndPorts and is the same for every
// cluster's mongot ConfigMap. Per-cluster differentiation lives at the Envoy /
// proxy-svc layer, not at the mongot seed list.
//
// The structural "same for every cluster" guarantee at the rendering layer
// lives in mongodbsearch_reconcile_helper.go's buildReplicaSetPlan, which
// calls HostSeeds("") exactly once and reuses the resulting slice in every
// per-cluster reconcileUnit's mongotConfigFn. This test pins the source-side
// half of that contract: HostSeeds returns the top-level HostAndPorts verbatim
// for the only valid (replica-set / non-sharded) call.
func TestExternalSearchSource_HostSeeds_ReturnsTopLevelHostAndPorts(t *testing.T) {
	wantHosts := []string{
		"rs-0.example:27017",
		"rs-1.example:27017",
		"rs-2.example:27017",
	}
	src := NewExternalSearchSource("ns", &searchv1.ExternalMongoDBSource{
		HostAndPorts: wantHosts,
	})

	got, err := src.HostSeeds("")
	require.NoError(t, err)
	assert.Equal(t, wantHosts, got,
		"replica-set HostSeeds must return the top-level external.hostAndPorts; "+
			"this list is rendered into every cluster's mongot ConfigMap")
}
