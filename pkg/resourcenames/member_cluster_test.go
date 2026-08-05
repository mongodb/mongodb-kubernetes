package resourcenames

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestResourceNameForCluster(t *testing.T) {
	t.Run("returns metadata.name for known clusters", func(t *testing.T) {
		SetResourceNames(map[string]string{"gke_proj_zone_cl": "gke-proj-zone-cl"})
		t.Cleanup(func() { SetResourceNames(nil) })

		assert.Equal(t, "gke-proj-zone-cl", ResourceNameForCluster("gke_proj_zone_cl"))
	})

	t.Run("falls back to clusterName when registry was never set", func(t *testing.T) {
		SetResourceNames(nil)

		assert.Equal(t, "some-cluster", ResourceNameForCluster("some-cluster"))
	})

	t.Run("falls back to clusterName for unknown entries", func(t *testing.T) {
		SetResourceNames(map[string]string{"other": "other"})
		t.Cleanup(func() { SetResourceNames(nil) })

		assert.Equal(t, "unknown-cluster", ResourceNameForCluster("unknown-cluster"))
	})

	t.Run("reset with nil clears previous entries", func(t *testing.T) {
		SetResourceNames(map[string]string{"a": "a-cr"})
		SetResourceNames(nil)

		assert.Equal(t, "a", ResourceNameForCluster("a"))
	})
}

func TestWorkloadServiceAccountName(t *testing.T) {
	SetResourceNames(map[string]string{"gke_proj_zone_cl": "gke-proj-zone-cl"})
	t.Cleanup(func() { SetResourceNames(nil) })

	workloads := map[string]struct {
		sa        workloadServiceAccount
		fixedName string
		suffix    string
	}{
		"appdb":         {sa: WorkloadAppDBServiceAccount, fixedName: util.AppDBServiceAccount, suffix: "appdb"},
		"database pods": {sa: WorkloadDatabasePodsServiceAccount, fixedName: util.MongoDBServiceAccount, suffix: "database-pods"},
		"ops manager":   {sa: WorkloadOpsManagerServiceAccount, fixedName: util.OpsManagerServiceAccount, suffix: "ops-manager"},
	}

	for name, w := range workloads {
		t.Run(name+" base install returns fixed helm-install name", func(t *testing.T) {
			assert.Equal(t, w.fixedName, w.sa.Name("gke_proj_zone_cl", true))
		})
		t.Run(name+" member cluster returns member-scoped name", func(t *testing.T) {
			assert.Equal(t, "mck-member-gke-proj-zone-cl-"+w.suffix, w.sa.Name("gke_proj_zone_cl", false))
		})
		t.Run(name+" member cluster with unset registry falls back to clusterName", func(t *testing.T) {
			SetResourceNames(nil)
			t.Cleanup(func() { SetResourceNames(map[string]string{"gke_proj_zone_cl": "gke-proj-zone-cl"}) })

			assert.Equal(t, "mck-member-some-cluster-"+w.suffix, w.sa.Name("some-cluster", false))
		})
	}
}
