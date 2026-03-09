package migrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
)

func TestValidation_OneDeploymentPerProject_SingleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "single-RS config should not produce errors: %s", r.Message)
	}
}

func TestValidation_OneDeploymentPerProject_MultipleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "multi_rs_automation_config.json")

	results := ValidateMigration(ac, nil, nil)
	hasMultipleDeploymentsError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "deployments") {
			hasMultipleDeploymentsError = true
			assert.Contains(t, r.Message, "split the project")
		}
	}
	assert.True(t, hasMultipleDeploymentsError, "expected error when project has multiple replica sets")
}

func TestValidation_OneDeploymentPerProject_SingleSharded(t *testing.T) {
	ac := loadTestAutomationConfig(t, "sharded_with_rs_automation_config.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "single-sharded config should not produce errors: %s", r.Message)
	}
}

func TestValidation_NoReplicaSets(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "no replicaSets") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when replicaSets is empty")
}

func TestValidation_MemberReferencesUnknownProcess(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs",
				"members": []interface{}{
					map[string]interface{}{"host": "unknown-process"},
				},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "not found") {
			hasError = true
			assert.Contains(t, r.Message, "unknown-process")
		}
	}
	assert.True(t, hasError, "expected error when member references unknown process")
}


func TestValidation_ReplicaSetWithNoMembers(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":     "my-rs",
				"members": []interface{}{},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "no members") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when replica set has no members")
}

func TestValidation_ProcessesHaveNoVersion(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{"name": "host-0", "hostname": "host-0"},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs",
				"members": []interface{}{
					map[string]interface{}{"host": "host-0"},
				},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "version") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when no process has a version")
}

func TestValidation_NonDefaultKeyFile(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Auth.KeyFile = "/custom/path/keyfile"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "keyFile") {
			hasError = true
			assert.Contains(t, r.Message, "/custom/path/keyfile")
		}
	}
	assert.True(t, hasError, "expected error when keyFile differs from default")
}

func TestValidation_NonDefaultAutoPEMKeyFilePath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.AgentSSL.AutoPEMKeyFilePath = "/etc/mongodb-mms/agent.pem"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "autoPEMKeyFilePath") {
			hasError = true
			assert.Contains(t, r.Message, "/etc/mongodb-mms/agent.pem")
		}
	}
	assert.True(t, hasError, "expected error when autoPEMKeyFilePath is set")
}

func TestValidation_NonDefaultCAFilePath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.AgentSSL.CAFilePath = "/etc/ssl/ca.pem"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "CAFilePath") {
			hasError = true
			assert.Contains(t, r.Message, "/etc/ssl/ca.pem")
		}
	}
	assert.True(t, hasError, "expected error when CAFilePath differs from default")
}

func TestValidation_NonDefaultDownloadBase(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	options := ac.Deployment["options"].(map[string]interface{})
	options["downloadBase"] = "/opt/mongodb/automation"
	ac.Deployment["options"] = options

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "downloadBase") {
			hasError = true
			assert.Contains(t, r.Message, "/opt/mongodb/automation")
		}
	}
	assert.True(t, hasError, "expected error when downloadBase differs from default")
}

func TestValidation_NonDefaultKeyFileWindows(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Auth.KeyFileWindows = "C:\\custom\\keyfile"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "keyFileWindows") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when keyFileWindows differs from default")
}

func TestValidation_NonDefaultAuthSchemaVersion(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	processes := getSlice(ac.Deployment, "processes")
	proc := processes[0].(map[string]interface{})
	proc["authSchemaVersion"] = 3

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "authSchemaVersion") {
			hasError = true
			assert.Contains(t, r.Message, "3")
		}
	}
	assert.True(t, hasError, "expected error when authSchemaVersion differs from default")
}

func TestValidation_NonDefaultProtocolVersion(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	replicaSets := getReplicaSets(ac.Deployment)
	replicaSets[0]["protocolVersion"] = "0"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "protocolVersion") {
			hasError = true
			assert.Contains(t, r.Message, `"0"`)
		}
	}
	assert.True(t, hasError, "expected error when protocolVersion differs from default")
}

func TestValidation_NonDefaultMonitoringAgentLogPath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	monitoringConfig := &om.MonitoringAgentConfig{
		BackingMap: map[string]interface{}{"logPath": "/var/log/mongodb/monitoring.log"},
	}

	results := ValidateMigration(ac, monitoringConfig, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "monitoringAgentConfig.logPath") {
			hasError = true
			assert.Contains(t, r.Message, "/var/log/mongodb/monitoring.log")
		}
	}
	assert.True(t, hasError, "expected error when monitoring agent logPath differs from default")
}

func TestValidation_NonDefaultBackupAgentLogPath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	backupConfig := &om.BackupAgentConfig{
		BackingMap: map[string]interface{}{"logPath": "/var/log/mongodb/backup.log"},
	}

	results := ValidateMigration(ac, nil, backupConfig)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "backupAgentConfig.logPath") {
			hasError = true
			assert.Contains(t, r.Message, "/var/log/mongodb/backup.log")
		}
	}
	assert.True(t, hasError, "expected error when backup agent logPath differs from default")
}

func TestValidation_ValidConfig_NoErrors(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "valid config should not produce errors: %s", r.Message)
	}
}

func TestValidation_LdapBindMethodSASL(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Ldap = &ldap.Ldap{
		Servers:    "ldap.example.com:636",
		BindMethod: "sasl",
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "bindMethod") {
			hasWarning = true
			assert.Contains(t, r.Message, "sasl")
			assert.Contains(t, r.Message, "simple")
		}
	}
	assert.True(t, hasWarning, "expected warning when LDAP bindMethod is not simple")
}

func TestValidation_LdapBindMethodSimple_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Ldap = &ldap.Ldap{
		Servers:    "ldap.example.com:636",
		BindMethod: "simple",
	}

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "bindMethod") {
			t.Errorf("unexpected warning/error about bindMethod: %s", r.Message)
		}
	}
}

func TestValidation_LdapCaFileContents(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Ldap = &ldap.Ldap{
		Servers:        "ldap.example.com:636",
		CaFileContents: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "LDAP CA") {
			hasWarning = true
			assert.Contains(t, r.Message, "ldap-ca")
			assert.Contains(t, r.Message, "ca.pem")
		}
	}
	assert.True(t, hasWarning, "expected warning when LDAP CA file contents exist")
}

func TestValidation_LdapNoCaFileContents_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Ldap = &ldap.Ldap{
		Servers: "ldap.example.com:636",
	}

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "LDAP CA") {
			t.Errorf("unexpected warning about LDAP CA: %s", r.Message)
		}
	}
}

func TestValidation_NilLdap_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	ac.Ldap = nil

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "LDAP") {
			t.Errorf("unexpected LDAP warning/error when LDAP is nil: %s", r.Message)
		}
	}
}

func TestValidation_NonDefaultDbPath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	processes := getSlice(ac.Deployment, "processes")
	proc := processes[0].(map[string]interface{})
	args := proc["args2_6"].(map[string]interface{})
	storage := args["storage"].(map[string]interface{})
	storage["dbPath"] = "/data/custom"

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "dbPath") {
			hasWarning = true
			assert.Contains(t, r.Message, "/data/custom")
		}
	}
	assert.True(t, hasWarning, "expected warning when dbPath is not /data")
}

func TestValidation_DefaultDbPath_NoWarning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name":     "host-0",
				"hostname": "host-0.example.com",
				"version":  "7.0.0",
				"args2_6": map[string]interface{}{
					"net":     map[string]interface{}{"port": 27017},
					"storage": map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "host-0"},
				},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "dbPath") {
			t.Errorf("unexpected warning about dbPath: %s", r.Message)
		}
	}
}

func TestValidation_AllowTLSMode(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	processes := getSlice(ac.Deployment, "processes")
	proc := processes[0].(map[string]interface{})
	args := proc["args2_6"].(map[string]interface{})
	args["net"] = map[string]interface{}{
		"port": 27017,
		"tls":  map[string]interface{}{"mode": "allowTLS"},
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "allowTLS") {
			hasWarning = true
			assert.Contains(t, r.Message, "additionalMongodConfig")
		}
	}
	assert.True(t, hasWarning, "expected warning when TLS mode is allowTLS")
}

func TestValidation_AllowSSLMode(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")
	processes := getSlice(ac.Deployment, "processes")
	proc := processes[0].(map[string]interface{})
	args := proc["args2_6"].(map[string]interface{})
	args["net"] = map[string]interface{}{
		"port": 27017,
		"ssl":  map[string]interface{}{"mode": "allowSSL"},
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "allowSSL") {
			hasWarning = true
		}
	}
	assert.True(t, hasWarning, "expected warning when TLS mode is allowSSL")
}

func TestValidation_RequireTLS_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "replicaset_automation_config.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "allowTLS") || strings.Contains(r.Message, "allowSSL") {
			t.Errorf("unexpected warning about allow TLS mode: %s", r.Message)
		}
	}
}

func TestValidation_NoTLS_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name":     "host-0",
				"hostname": "host-0.example.com",
				"version":  "6.0.5",
				"args2_6": map[string]interface{}{
					"net":     map[string]interface{}{"port": 27017},
					"storage": map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "host-0"},
				},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	hasNoTLSWarning := false
	hasSecurityTLSWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "additionalMongodConfig.net.tls.mode") {
			hasNoTLSWarning = true
		}
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "spec.security.tls") {
			hasSecurityTLSWarning = true
		}
	}
	assert.True(t, hasNoTLSWarning, "expected warning about additionalMongodConfig.net.tls.mode for no-TLS deployment")
	assert.True(t, hasSecurityTLSWarning, "expected warning about spec.security.tls for no-TLS deployment")
}

func TestValidation_TLSDisabled_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name":     "host-0",
				"hostname": "host-0.example.com",
				"version":  "6.0.5",
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{
						"port": 27017,
						"tls":  map[string]interface{}{"mode": "disabled"},
					},
					"storage": map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "host-0"},
				},
			},
		},
	})

	results := ValidateMigration(ac, nil, nil)
	hasNoTLSWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "additionalMongodConfig.net.tls.mode") {
			hasNoTLSWarning = true
		}
	}
	assert.True(t, hasNoTLSWarning, "expected warning about TLS mode for disabled TLS")
}

