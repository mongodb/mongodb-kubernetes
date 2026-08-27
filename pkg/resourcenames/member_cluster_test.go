package resourcenames

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestWorkloadServiceAccountName(t *testing.T) {
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
			assert.Equal(t, w.fixedName, w.sa.Name("gke-proj-zone-cl", true))
		})
		t.Run(name+" member cluster returns member-scoped name", func(t *testing.T) {
			assert.Equal(t, "mck-member-gke-proj-zone-cl-"+w.suffix, w.sa.Name("gke-proj-zone-cl", false))
		})
	}
}
