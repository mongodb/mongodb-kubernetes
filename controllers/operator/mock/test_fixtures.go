package mock

import (
	"os"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// nolint:forbidigo
func InitDefaultEnvVariables() {
	os.Setenv(util.NonStaticDatabaseEnterpriseImage, "mongodb-enterprise-database")
	os.Setenv(util.AutomationAgentImagePullPolicy, "Never")
	os.Setenv(util.OpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-ops-manager")
	os.Setenv(util.InitOpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-init-ops-manager")
	os.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	os.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	os.Setenv(util.OpsManagerPullPolicy, "Never")
	os.Setenv(util.OmOperatorEnv, "test")
	os.Setenv(util.PodWaitSecondsEnv, "1")
	os.Setenv(util.PodWaitRetriesEnv, "2")
	os.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	os.Setenv(util.BackupDisableWaitRetriesEnv, "3")
	os.Unsetenv(util.ManagedSecurityContextEnv)
	os.Unsetenv(util.ImagePullSecrets)
}
