package migrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

func TestValidation_OneDeploymentPerProject_SingleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "single-RS config should not produce errors: %s", r.Message)
	}
}

func TestValidation_OneDeploymentPerProject_MultipleRS(t *testing.T) {
	ac := loadTestAutomationConfig(t, "multi_replicaset.json")

	results := ValidateMigration(ac, nil, nil)
	hasMultipleDeploymentsError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "deployments") {
			hasMultipleDeploymentsError = true
			assert.Contains(t, r.Message, "before migrating")
		}
	}
	assert.True(t, hasMultipleDeploymentsError, "expected error when project has multiple replica sets")
}

func TestValidation_OneDeploymentPerProject_SingleSharded(t *testing.T) {
	ac := loadTestAutomationConfig(t, "sharded_cluster.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "single-sharded config should not produce errors: %s", r.Message)
	}
}

func TestValidation_NoReplicaSets(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
		"sharding":    []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "No replica sets found") {
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
					map[string]interface{}{"host": "unknown-process", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
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
		"sharding": []interface{}{},
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

func TestValidation_NonDefaultKeyFile(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.AgentSSL.AutoPEMKeyFilePath = "/etc/mongodb-mms/agent.pem"

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "autoPEMKeyFilePath") {
			hasWarning = true
			assert.Contains(t, r.Message, "/etc/mongodb-mms/agent.pem")
		}
	}
	assert.True(t, hasWarning, "expected error when autoPEMKeyFilePath is set")
}

func TestValidation_NonDefaultCAFilePath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.AgentSSL.CAFilePath = "/etc/ssl/ca.pem"

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "CAFilePath") {
			hasWarning = true
			assert.Contains(t, r.Message, "/etc/ssl/ca.pem")
		}
	}
	assert.True(t, hasWarning, "expected error when CAFilePath differs from default")
}

func TestValidation_NonDefaultDownloadBase(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	processes := ac.Deployment.GetProcesses()
	processes[0]["authSchemaVersion"] = 3

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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	replicaSets := ac.Deployment.GetReplicaSets()
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	monitoringConfig := &om.MonitoringAgentConfig{
		BackingMap: map[string]interface{}{"logPath": "/var/log/mongodb/monitoring.log"},
	}

	results := ValidateMigration(ac, &ProjectAgentConfigs{MonitoringConfig: monitoringConfig}, nil)
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	backupConfig := &om.BackupAgentConfig{
		BackingMap: map[string]interface{}{"logPath": "/var/log/mongodb/backup.log"},
	}

	results := ValidateMigration(ac, &ProjectAgentConfigs{BackupConfig: backupConfig}, nil)
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		assert.NotEqual(t, SeverityError, r.Severity, "valid config should not produce errors: %s", r.Message)
	}
}

func TestValidation_LdapBindMethodSASL(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.Ldap = &ldap.Ldap{
		Servers:    "ldap.example.com:636",
		BindMethod: "sasl",
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "bindMethod") {
			hasWarning = true
			assert.Contains(t, r.Message, "sasl")
			assert.Contains(t, r.Message, "simple")
		}
	}
	assert.True(t, hasWarning, "expected error when LDAP bindMethod is not simple")
}

func TestValidation_LdapBindMethodSimple_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.Ldap = nil

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "LDAP") {
			t.Errorf("unexpected LDAP warning/error when LDAP is nil: %s", r.Message)
		}
	}
}

func TestValidation_NonDefaultDbPath(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	proc := ac.Deployment.GetProcesses()[0]
	args := proc.Args()
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
				"name":        "host-0",
				"hostname":    "host-0.example.com",
				"processType": "mongod",
				"version":     "7.0.0",
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
					map[string]interface{}{"host": "host-0", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "dbPath") {
			t.Errorf("unexpected warning about dbPath: %s", r.Message)
		}
	}
}

func TestValidation_AllowTLSMode(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	proc := ac.Deployment.GetProcesses()[0]
	args := proc.Args()
	args["net"] = map[string]interface{}{
		"port": 27017,
		"tls":  map[string]interface{}{"mode": "allowTLS"},
	}

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "allowTLS") {
			hasWarning = true
			assert.Contains(t, r.Message, "requireTLS")
		}
	}
	assert.True(t, hasWarning, "expected warning when TLS mode is allowTLS")
}

func TestValidation_AllowSSLMode(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	proc := ac.Deployment.GetProcesses()[0]
	args := proc.Args()
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
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

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
				"name":        "host-0",
				"hostname":    "host-0.example.com",
				"processType": "mongod",
				"version":     "6.0.5",
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
					map[string]interface{}{"host": "host-0", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasNoTLSWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "net.tls.mode") {
			hasNoTLSWarning = true
		}
	}
	assert.True(t, hasNoTLSWarning, "expected warning about net.tls.mode for no-TLS deployment")
}

func TestValidation_TLSDisabled_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name":        "host-0",
				"hostname":    "host-0.example.com",
				"processType": "mongod",
				"version":     "6.0.5",
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
					map[string]interface{}{"host": "host-0", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasNoTLSWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "net.tls.mode") {
			hasNoTLSWarning = true
		}
	}
	assert.True(t, hasNoTLSWarning, "expected warning about TLS mode for disabled TLS")
}

func TestValidation_TLSModeNull_Error(t *testing.T) {
	// Process has net.tls section but mode is null/missing — not allowed in AC.
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "host-0", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{
						"port": 27017,
						"tls":  map[string]interface{}{}, // mode missing
					},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs", "protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "host-0", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})
	results := ValidateMigration(ac, nil, nil)
	var hasError bool
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "mode is null or missing") {
			hasError = true
			break
		}
	}
	assert.True(t, hasError, "expected error when net.tls exists but mode is null or missing")
}

func TestValidation_HeterogeneousProcessConfig_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27018},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27019},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasPortWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "net.port") && strings.Contains(r.Message, "excluded") {
			hasPortWarning = true
		}
	}
	assert.True(t, hasPortWarning, "expected warning about net.port being excluded when processes have different ports")
}

func TestValidation_HomogeneousProcessConfig_NoWarning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27018},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data1"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27018},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data2"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "excluded") && strings.Contains(r.Message, "additionalMongodConfig") {
			t.Errorf("unexpected heterogeneous config warning: %s", r.Message)
		}
	}
}

func TestValidation_HeterogeneousStorageEngine_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "8.0.4-ent",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data", "engine": "inMemory"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "8.0.4-ent",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasEngineWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "storage.engine") && strings.Contains(r.Message, "excluded") {
			hasEngineWarning = true
		}
	}
	assert.True(t, hasEngineWarning, "expected warning about storage.engine being excluded when one process has inMemory and another has default wiredTiger")
}

func TestValidation_HeterogeneousConfig_MultipleFields_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "8.0.4-ent",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27018},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data", "engine": "inMemory"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "8.0.4-ent",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27019},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	excludedFields := map[string]bool{}
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "excluded") {
			if strings.Contains(r.Message, "net.port") {
				excludedFields["net.port"] = true
			}
			if strings.Contains(r.Message, "storage.engine") {
				excludedFields["storage.engine"] = true
			}
		}
	}
	assert.True(t, excludedFields["net.port"], "expected warning about net.port being excluded")
	assert.True(t, excludedFields["storage.engine"], "expected warning about storage.engine being excluded")
}

func TestValidation_DifferentOperatorManagedFields_NoWarning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
					"systemLog":   map[string]interface{}{"destination": "file", "path": "/var/log/rs-0.log"},
					"security":    map[string]interface{}{"clusterAuthMode": "x509"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"storage":     map[string]interface{}{"dbPath": "/data"},
					"systemLog":   map[string]interface{}{"destination": "file", "path": "/var/log/rs-1.log"},
					"security":    map[string]interface{}{"clusterAuthMode": "x509"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id":             "my-rs",
				"protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "excluded") && strings.Contains(r.Message, "additionalMongodConfig") {
			t.Errorf("processes differ only in operator-managed fields (systemLog, security) but got heterogeneous warning: %s", r.Message)
		}
	}
}

func TestValidation_EmptyAutoUser(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.Auth.AutoUser = ""

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "autoUser") && strings.Contains(r.Message, "empty") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when autoUser is empty and auth is enabled")
}

func TestValidation_AutoUserNotInUsersWanted(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.Auth.AutoUser = "nonexistent-agent"

	results := ValidateMigration(ac, nil, nil)
	hasError := false
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "nonexistent-agent") && strings.Contains(r.Message, "usersWanted") {
			hasError = true
		}
	}
	assert.True(t, hasError, "expected error when autoUser has no matching entry in usersWanted")
}

func TestValidation_AutoUserMatchesUsersWanted_NoError(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "autoUser") {
			t.Errorf("valid autoUser should not produce errors: %s", r.Message)
		}
	}
}

func TestValidation_X509AutoUser_NotInUsersWanted_NoError(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")
	ac.Auth.AutoUser = "CN=mms-automation-agent,OU=test,O=cluster.local"
	ac.Auth.AutoAuthMechanism = "MONGODB-X509"
	ac.Auth.Users = nil

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if r.Severity == SeverityError && strings.Contains(r.Message, "autoUser") && strings.Contains(r.Message, "usersWanted") {
			t.Errorf("X509 autoUser should not require a matching usersWanted entry: %s", r.Message)
		}
	}
}

func TestValidation_VersionConsistency_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "7.0.0",
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{"port": 27017}, "storage": map[string]interface{}{"dbPath": "/data"},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
				},
			},
			map[string]interface{}{
				"name": "rs-1", "processType": "mongod", "version": "8.0.0",
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{"port": 27017}, "storage": map[string]interface{}{"dbPath": "/data"},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs", "protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
					map[string]interface{}{"host": "rs-1", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	results := ValidateMigration(ac, nil, nil)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "different MongoDB versions") {
			hasWarning = true
		}
	}
	assert.True(t, hasWarning, "expected warning when members have different versions")
}

func TestValidation_VersionConsistency_NoWarning(t *testing.T) {
	ac := loadTestAutomationConfig(t, "singlecluster/replicaset/full.json")

	results := ValidateMigration(ac, nil, nil)
	for _, r := range results {
		if strings.Contains(r.Message, "different MongoDB versions") {
			t.Errorf("unexpected version consistency warning: %s", r.Message)
		}
	}
}

func TestValidation_AgentConfigDrift_Warning(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes": []interface{}{
			map[string]interface{}{
				"name": "rs-0", "processType": "mongod", "version": "7.0.0",
				"logRotate": map[string]interface{}{"sizeThresholdMB": 500, "timeThresholdHrs": 12},
				"args2_6": map[string]interface{}{
					"net": map[string]interface{}{"port": 27017}, "storage": map[string]interface{}{"dbPath": "/data"},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
				},
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs", "protocolVersion": "1",
				"members": []interface{}{
					map[string]interface{}{"host": "rs-0", "tags": map[string]string{}},
				},
			},
		},
		"sharding": []interface{}{},
	})

	projectProcessConfigs := &ProjectProcessConfigs{
		SystemLogRotate: &automationconfig.AcLogRotate{
			LogRotate: automationconfig.LogRotate{
				TimeThresholdHrs: 24,
				NumUncompressed:  5,
				NumTotal:         10,
			},
			SizeThresholdMB:    1000,
			PercentOfDiskspace: 0.02,
		},
	}

	results := ValidateMigration(ac, nil, projectProcessConfigs)
	hasWarning := false
	for _, r := range results {
		if r.Severity == SeverityWarning && strings.Contains(r.Message, "logRotate") && strings.Contains(r.Message, "differs from project-level") {
			hasWarning = true
		}
	}
	assert.True(t, hasWarning, "expected warning when per-process logRotate differs from project-level setting")
}



