package migrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidation_OneDeploymentPerProject_SingleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	blockers := ValidateMigrationBlockers(ac)
	for _, b := range blockers {
		assert.NotEqual(t, SeverityError, b.Severity, "single-RS config should not produce errors: %s", b.Message)
	}
}

func TestValidation_OneDeploymentPerProject_MultipleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "multi_rs_automation_config.json")

	blockers := ValidateMigrationBlockers(ac)
	hasMultipleDeploymentsError := false
	for _, b := range blockers {
		if b.Severity == SeverityError && strings.Contains(b.Message, "deployments") {
			hasMultipleDeploymentsError = true
			assert.Contains(t, b.Message, "split the project")
		}
	}
	assert.True(t, hasMultipleDeploymentsError, "expected error when project has multiple replica sets")
}

func TestValidation_OneDeploymentPerProject_SingleSharded(t *testing.T) {
	ac := loadTestAutomationConfig(t, "sharded_with_rs_automation_config.json")

	blockers := ValidateMigrationBlockers(ac)
	for _, b := range blockers {
		assert.NotEqual(t, SeverityError, b.Severity, "single-sharded config should not produce errors: %s", b.Message)
	}
}

