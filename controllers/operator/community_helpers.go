package operator

// community_helpers.go contains helpers that were originally defined in the
// mongodb-community-operator/controllers package but are also needed by the
// Enterprise operator. The MCO copies are kept unchanged (DUPLICATE strategy)
// because MCO uses them internally as well.

import (
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

const (
	// ListenAddress is the default Prometheus listen address.
	// Keep in sync with mongodb-community-operator/controllers.ListenAddress.
	ListenAddress = "0.0.0.0"
)

// OverrideToAutomationConfig turns an automation config override from the
// resource spec into an automation config which can be used to merge.
// Keep in sync with mongodb-community-operator/controllers.OverrideToAutomationConfig.
func OverrideToAutomationConfig(override mdbcv1.AutomationConfigOverride) automationconfig.AutomationConfig {
	var processes []automationconfig.Process
	for _, o := range override.Processes {
		p := automationconfig.Process{
			Name:      o.Name,
			Disabled:  o.Disabled,
			LogRotate: automationconfig.ConvertCrdLogRotateToAC(o.LogRotate),
		}
		processes = append(processes, p)
	}

	return automationconfig.AutomationConfig{
		Processes: processes,
	}
}
