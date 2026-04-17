package migrate

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func TestFetchAndValidate_ValidDeployment(t *testing.T) {
	d := om.Deployment(map[string]interface{}{
		"processes": []interface{}{
			map[string]interface{}{
				"name":        "host-0",
				"processType": "mongod",
				"hostname":    "host-0.example.com",
				"args2_6": map[string]interface{}{
					"net":         map[string]interface{}{"port": 27017},
					"replication": map[string]interface{}{"replSetName": "my-rs"},
					"systemLog": map[string]interface{}{
						"destination": "file",
						"path":        "/var/log/mongodb-mms-automation/mongodb.log",
					},
				},
				"authSchemaVersion": om.CalculateAuthSchemaVersion(),
			},
		},
		"replicaSets": []interface{}{
			map[string]interface{}{
				"_id": "my-rs",
				"members": []interface{}{
					map[string]interface{}{"_id": 0, "host": "host-0", "votes": 1, "priority": 1},
				},
			},
		},
		"sharding": []interface{}{},
	})
	conn := om.NewMockedOmConnection(d)
	ac, projectConfigs, sourceProcess, err := fetchAndValidate(conn)
	require.NoError(t, err)
	require.NotNil(t, ac)
	require.NotNil(t, projectConfigs)
	require.NotNil(t, sourceProcess)
	assert.Equal(t, "host-0", sourceProcess.Name())
}

func TestFetchAndValidate_ValidationError(t *testing.T) {
	// No processes, no replica sets -> validation will fail with "No replica sets found"
	d := om.Deployment(map[string]interface{}{
		"processes":   []interface{}{},
		"replicaSets": []interface{}{},
		"sharding":    []interface{}{},
	})
	conn := om.NewMockedOmConnection(d)
	_, _, _, err := fetchAndValidate(conn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestPrintValidationResults_CountsErrors(t *testing.T) {
	results := []ValidationResult{
		{Severity: SeverityWarning, Message: "some warning"},
		{Severity: SeverityError, Message: "first error"},
		{Severity: SeverityError, Message: "second error"},
		{Severity: SeverityWarning, Message: "another warning"},
	}
	var buf bytes.Buffer
	count := printValidationResults(&buf, results)
	assert.Equal(t, 2, count)
	assert.Contains(t, buf.String(), "[WARNING] some warning")
	assert.Contains(t, buf.String(), "[ERROR] first error")
	assert.Contains(t, buf.String(), "[ERROR] second error")
}

func TestPrintValidationResults_NoResults(t *testing.T) {
	var buf bytes.Buffer
	count := printValidationResults(&buf, nil)
	assert.Equal(t, 0, count)
	assert.Empty(t, buf.String())
}
