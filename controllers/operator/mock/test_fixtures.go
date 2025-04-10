package mock

import (
	"os"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// nolint:forbidigo
func InitDefaultEnvVariables() {
	_ = os.Setenv(util.NonStaticDatabaseEnterpriseImage, "mongodb-enterprise-database")
	_ = os.Setenv(util.AutomationAgentImagePullPolicy, "Never")
	_ = os.Setenv(util.OpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-ops-manager")
	_ = os.Setenv(util.InitOpsManagerImageUrl, "quay.io/mongodb/mongodb-enterprise-init-ops-manager")
	_ = os.Setenv(util.InitAppdbImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-appdb")
	_ = os.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	_ = os.Setenv(util.OpsManagerPullPolicy, "Never")
	_ = os.Setenv(util.OmOperatorEnv, "test")
	_ = os.Setenv(util.PodWaitSecondsEnv, "1")
	_ = os.Setenv(util.PodWaitRetriesEnv, "2")
	_ = os.Setenv(util.BackupDisableWaitSecondsEnv, "1")
	_ = os.Setenv(util.BackupDisableWaitRetriesEnv, "3")
	_ = os.Unsetenv(util.ManagedSecurityContextEnv)
	_ = os.Unsetenv(util.ImagePullSecrets)
}
