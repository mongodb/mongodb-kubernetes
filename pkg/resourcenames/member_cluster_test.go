package resourcenames

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestWorkloadServiceAccountName(t *testing.T) {
	workloads := map[string]struct {
		sa              workloadServiceAccount
		baseInstallName string
		memberName      string
	}{
		"appdb":         {sa: WorkloadAppDBServiceAccount, baseInstallName: util.AppDBServiceAccount, memberName: "mck-member-appdb"},
		"database pods": {sa: WorkloadDatabasePodsServiceAccount, baseInstallName: util.MongoDBServiceAccount, memberName: "mck-member-database-pods"},
		"ops manager":   {sa: WorkloadOpsManagerServiceAccount, baseInstallName: util.OpsManagerServiceAccount, memberName: "mck-member-ops-manager"},
	}

	for name, w := range workloads {
		t.Run(name+" base install returns fixed helm-install name", func(t *testing.T) {
			assert.Equal(t, w.baseInstallName, w.sa.Name(true))
		})
		t.Run(name+" member cluster returns fixed member-scoped name", func(t *testing.T) {
			assert.Equal(t, w.memberName, w.sa.Name(false))
		})
	}
}
