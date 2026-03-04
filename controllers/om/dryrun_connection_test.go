package om_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	om "github.com/mongodb/mongodb-kubernetes/controllers/om"
)

// deploymentFromJSONDryRun builds a Deployment from a JSON string, giving realistic
// []interface{} types for arrays — matching what the OM API returns after JSON parsing.
func deploymentFromJSONDryRun(t *testing.T, raw string) om.Deployment {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return om.Deployment(m)
}

// baseDryRunDeployment returns a minimal 1-process deployment as if read from OM.
func baseDryRunDeployment(t *testing.T) om.Deployment {
	return deploymentFromJSONDryRun(t, `{
		"processes": [
			{"name": "vm1-27017", "hostname": "vm1.example.com", "processType": "mongod"}
		],
		"replicaSets": [],
		"auth": {},
		"tls": {}
	}`)
}

// TestDryRunConnection_ReadUpdateDeployment_NoWrite verifies that fn is applied to the
// working copy but nothing is written back to the underlying connection.
func TestDryRunConnection_ReadUpdateDeployment_NoWrite(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		processes := d["processes"].([]interface{})
		d["processes"] = append(processes, map[string]interface{}{
			"name": "mdb-0-27017", "hostname": "mdb-0.svc.cluster.local",
		})
		return nil
	}, log)

	require.NoError(t, err)
	// The mocked connection's deployment must not have been mutated.
	assert.Len(t, mocked.GetDeployment()["processes"].([]interface{}), 1)
}

// TestDryRunConnection_MultipleCallsChain verifies that successive ReadUpdateDeployment
// calls accumulate changes — the second fn sees the result of the first.
func TestDryRunConnection_MultipleCallsChain(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	// First call: rename the existing process.
	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		d["processes"].([]interface{})[0].(map[string]interface{})["name"] = "renamed-27017"
		return nil
	}, log)
	require.NoError(t, err)

	// Second call: verify the rename is visible.
	err = dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		name := d["processes"].([]interface{})[0].(map[string]interface{})["name"]
		assert.Equal(t, "renamed-27017", name)
		return nil
	}, log)
	require.NoError(t, err)
}

// TestDryRunConnection_Result_Added verifies that a process added by the reconcile fn
// appears in result.Added.
func TestDryRunConnection_Result_Added(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		processes := d["processes"].([]interface{})
		d["processes"] = append(processes, map[string]interface{}{
			"name": "mdb-0-27017", "hostname": "mdb-0.svc.cluster.local",
		})
		return nil
	}, log)
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.NotEmpty(t, result.Added)
	assert.Empty(t, result.Removed)
}

// TestDryRunConnection_Result_Modified verifies that a modified field appears in
// result.Modified.
func TestDryRunConnection_Result_Modified(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		d["processes"].([]interface{})[0].(map[string]interface{})["processType"] = "mongos"
		return nil
	}, log)
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.NotEmpty(t, result.Modified)
}

// TestDryRunConnection_Result_WarnOnProcessRemoval verifies that removing existing OM
// processes triggers a warning in the result.
func TestDryRunConnection_Result_WarnOnProcessRemoval(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		d["processes"] = []interface{}{}
		return nil
	}, log)
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.NotEmpty(t, result.Warning)
	assert.NotEmpty(t, result.Removed)
}

// TestDryRunConnection_Result_NoFalseModificationsWithTypedValues verifies that values
// which are semantically equal but stored with different Go types in the working
// deployment — as happens after the operator's reconcile logic runs — are NOT reported
// as modifications by the diff.
//
// Concretely, the operator inserts:
//   - float32 for replica-set member priority  (JSON-parsed baseline stores float64)
//   - int for replica-set member votes         (baseline: float64)
//   - MongoType (named string) for processType (baseline: plain string)
//   - map[string]string{} for member tags      (baseline: map[string]interface{}{})
//
// Before the fix, r3labs/diff reported these as UPDATE entries whose "from" and "to"
// printed identically (e.g. "from: 1, to: 1"), cluttering the diff output.
func TestDryRunConnection_Result_NoFalseModificationsWithTypedValues(t *testing.T) {
	dep := deploymentFromJSONDryRun(t, `{
		"processes": [
			{"name": "rs001-pv-0", "hostname": "rs001-pv-0.svc", "processType": "mongod"}
		],
		"replicaSets": [{
			"_id": "rs001",
			"members": [{
				"_id": 0,
				"host": "rs001-pv-0",
				"priority": 1,
				"votes": 1,
				"arbiterOnly": false,
				"buildIndexes": true,
				"hidden": false,
				"secondaryDelaySecs": 0,
				"tags": {}
			}]
		}],
		"auth": {},
		"tls": {}
	}`)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	// Re-write the deployment with types the operator uses — values are semantically
	// identical to the JSON-parsed baseline but have different runtime Go types.
	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		d["processes"] = []om.Process{
			{
				"name":        "rs001-pv-0",
				"hostname":    "rs001-pv-0.svc",
				"processType": om.ProcessTypeMongod, // MongoType (named string), not plain string
			},
		}
		d["replicaSets"] = []om.ReplicaSet{
			{
				"_id": "rs001",
				"members": []om.ReplicaSetMember{
					{
						"_id":                0,            // int, not float64
						"host":               "rs001-pv-0",
						"priority":           float32(1.0), // float32, not float64
						"votes":              1,            // int, not float64
						"arbiterOnly":        false,
						"buildIndexes":       true,
						"hidden":             false,
						"secondaryDelaySecs": 0,
						"tags":               map[string]string{}, // map[string]string, not map[string]interface{}
					},
				},
			},
		}
		return nil
	}, log)
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.Empty(t, result.Modified, "type-only differences must not be reported as modifications")
	assert.Empty(t, result.Added, "no new entries were added")
	assert.Empty(t, result.Removed, "no entries were removed")
	assert.Empty(t, result.Warning)
}

// TestDryRunConnection_Result_OnlyNewMemberReported verifies that when a new replica-set
// member is appended to the working deployment, only the new member appears in Added and
// the existing unchanged member does NOT appear in Modified.
func TestDryRunConnection_Result_OnlyNewMemberReported(t *testing.T) {
	dep := deploymentFromJSONDryRun(t, `{
		"processes": [
			{"name": "rs001-pv-0", "hostname": "rs001-pv-0.svc", "processType": "mongod"}
		],
		"replicaSets": [{
			"_id": "rs001",
			"members": [{
				"_id": 0, "host": "rs001-pv-0",
				"priority": 1, "votes": 1,
				"arbiterOnly": false, "buildIndexes": true,
				"hidden": false, "secondaryDelaySecs": 0, "tags": {}
			}]
		}],
		"auth": {},
		"tls": {}
	}`)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		// Append a new process.
		processes := d["processes"].([]interface{})
		d["processes"] = append(processes, map[string]interface{}{
			"name": "rs001-pv-1", "hostname": "rs001-pv-1.svc", "processType": "mongod",
		})
		// Append a new member to the replica set.
		rsList := d["replicaSets"].([]interface{})
		rs := rsList[0].(map[string]interface{})
		members := rs["members"].([]interface{})
		rs["members"] = append(members, map[string]interface{}{
			"_id": 1, "host": "rs001-pv-1",
			"priority": 1, "votes": 1,
			"arbiterOnly": false, "buildIndexes": true,
			"hidden": false, "secondaryDelaySecs": 0, "tags": map[string]interface{}{},
		})
		return nil
	}, log)
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.NotEmpty(t, result.Added, "the new process and member must appear as added")
	assert.Empty(t, result.Modified, "unchanged existing member must not appear as modified")
	assert.Empty(t, result.Removed)
}

// TestDryRunConnection_Result_WarnOnProcessRemoval_TypedProcesses verifies that the
// process-removal warning is triggered correctly even when the working deployment
// stores processes as []Process (a typed slice) rather than []interface{}.
// Before the fix, hasProcessRemoval used a raw []interface{} type assertion that
// silently returned nil for []Process, causing a spurious warning on every reconcile.
func TestDryRunConnection_Result_WarnOnProcessRemoval_TypedProcesses(t *testing.T) {
	dep := deploymentFromJSONDryRun(t, `{
		"processes": [
			{"name": "vm-0", "hostname": "vm-0.example.com", "processType": "mongod"},
			{"name": "vm-1", "hostname": "vm-1.example.com", "processType": "mongod"}
		],
		"replicaSets": [],
		"auth": {},
		"tls": {}
	}`)
	mocked := om.NewMockedOmConnection(dep)

	t.Run("typed []Process with same count does not trigger warning", func(t *testing.T) {
		dryConn := om.NewDryRunConnection(mocked)
		err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
			// Replace []interface{} with the typed []Process the operator produces.
			d["processes"] = []om.Process{
				{"name": "vm-0", "hostname": "vm-0.example.com", "processType": om.ProcessTypeMongod},
				{"name": "vm-1", "hostname": "vm-1.example.com", "processType": om.ProcessTypeMongod},
			}
			return nil
		}, zap.S())
		require.NoError(t, err)

		result, err := dryConn.Result()
		require.NoError(t, err)
		assert.Empty(t, result.Warning, "same process count with typed slice must not trigger removal warning")
	})

	t.Run("typed []Process with fewer entries triggers warning", func(t *testing.T) {
		dryConn := om.NewDryRunConnection(mocked)
		err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
			d["processes"] = []om.Process{
				{"name": "vm-0", "hostname": "vm-0.example.com", "processType": om.ProcessTypeMongod},
			}
			return nil
		}, zap.S())
		require.NoError(t, err)

		result, err := dryConn.Result()
		require.NoError(t, err)
		assert.NotEmpty(t, result.Warning, "fewer processes in typed slice must trigger removal warning")
	})
}

// TestDryRunConnection_Result_FiltersNoisePaths verifies that fields which are always
// OM-managed or contain sensitive material are stripped from the diff output even when
// they differ between baseline and working deployment.
func TestDryRunConnection_Result_FiltersNoisePaths(t *testing.T) {
	tests := []struct {
		name     string
		baseline string // optional; defaults to a minimal empty deployment
		modify   func(d om.Deployment)
	}{
		{
			name: "mongoDbVersions changes are suppressed",
			modify: func(d om.Deployment) {
				d["mongoDbVersions"] = []interface{}{
					map[string]interface{}{"name": "8.0.0", "builds": []interface{}{}},
				}
			},
		},
		{
			// Realistic case: user already exists in OM, only the SCRAM credentials
			// are regenerated on reconcile (new salt each time). The path lands at
			// auth.usersWanted.0.scramSha256Creds.* which the FilterOut strips.
			name: "auth.usersWanted scramSha256Creds changes are suppressed",
			baseline: `{"processes":[],"replicaSets":[],"auth":{"usersWanted":[{
				"user":"alice","db":"admin",
				"scramSha256Creds":{"iterationCount":15000,"salt":"oldSalt==","serverKey":"oldKey==","storedKey":"oldStored=="}
			}]},"tls":{}}`,
			modify: func(d om.Deployment) {
				auth := d["auth"].(map[string]interface{})
				user := auth["usersWanted"].([]interface{})[0].(map[string]interface{})
				user["scramSha256Creds"] = map[string]interface{}{
					"iterationCount": 15000,
					"salt":           "newSalt==",
					"serverKey":      "newKey==",
					"storedKey":      "newStored==",
				}
			},
		},
		{
			// Realistic case: user already exists, only the hashed password changes.
			name: "auth.usersWanted pwd changes are suppressed",
			baseline: `{"processes":[],"replicaSets":[],"auth":{"usersWanted":[{
				"user":"alice","db":"admin","pwd":"oldHash"
			}]},"tls":{}}`,
			modify: func(d om.Deployment) {
				auth := d["auth"].(map[string]interface{})
				user := auth["usersWanted"].([]interface{})[0].(map[string]interface{})
				user["pwd"] = "newHash"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseline := `{"processes":[],"replicaSets":[],"auth":{},"tls":{}}`
			if tc.baseline != "" {
				baseline = tc.baseline
			}
			dep := deploymentFromJSONDryRun(t, baseline)
			mocked := om.NewMockedOmConnection(dep)
			dryConn := om.NewDryRunConnection(mocked)

			err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
				tc.modify(d)
				return nil
			}, zap.S())
			require.NoError(t, err)

			result, err := dryConn.Result()
			require.NoError(t, err)
			assert.Empty(t, result.Added)
			assert.Empty(t, result.Modified)
			assert.Empty(t, result.Removed)
		})
	}
}

// TestDryRunConnection_Result_IgnoresNilToEmptyTransitions verifies that changes between
// semantically equivalent empty values (nil ↔ [] ↔ {}) are suppressed from the diff output.
// These arise when the operator explicitly sets fields such as backupVersions or roles to
// an empty slice while the baseline omits the field entirely (nil), or vice-versa.
func TestDryRunConnection_Result_IgnoresNilToEmptyTransitions(t *testing.T) {
	tests := []struct {
		name     string
		baseline string
		modify   func(d om.Deployment)
	}{
		{
			name: "nil to empty slice (CREATE [] suppressed)",
			// baseline has no backupVersions key
			baseline: `{"processes":[],"replicaSets":[],"auth":{},"tls":{}}`,
			modify: func(d om.Deployment) {
				d["backupVersions"] = []interface{}{} // operator adds empty slice
			},
		},
		{
			name: "empty slice to nil (DELETE [] suppressed)",
			// baseline has backupVersions: []
			baseline: `{"processes":[],"replicaSets":[],"auth":{},"tls":{},"backupVersions":[]}`,
			modify: func(d om.Deployment) {
				delete(d, "backupVersions") // operator removes it entirely
			},
		},
		{
			name: "nil to empty map (CREATE {} suppressed)",
			baseline: `{"processes":[],"replicaSets":[],"auth":{},"tls":{}}`,
			modify: func(d om.Deployment) {
				d["roles"] = map[string]interface{}{} // operator adds empty map
			},
		},
		{
			name: "UPDATE nil→[] via overwrite suppressed",
			baseline: `{"processes":[],"replicaSets":[],"auth":{},"tls":{},"monitoringVersions":null}`,
			modify: func(d om.Deployment) {
				d["monitoringVersions"] = []interface{}{} // null → []
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dep := deploymentFromJSONDryRun(t, tc.baseline)
			mocked := om.NewMockedOmConnection(dep)
			dryConn := om.NewDryRunConnection(mocked)

			err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
				tc.modify(d)
				return nil
			}, zap.S())
			require.NoError(t, err)

			result, err := dryConn.Result()
			require.NoError(t, err)
			assert.Empty(t, result.Added, "empty-value additions must be suppressed")
			assert.Empty(t, result.Modified, "nil↔empty transitions must be suppressed")
			assert.Empty(t, result.Removed, "empty-value removals must be suppressed")
		})
	}
}

// TestDryRunConnection_Result_KeepsNonEmptyChanges verifies that the nil/empty filter
// does NOT suppress changes where one side has actual content.
func TestDryRunConnection_Result_KeepsNonEmptyChanges(t *testing.T) {
	dep := deploymentFromJSONDryRun(t, `{"processes":[],"replicaSets":[],"auth":{},"tls":{}}`)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)

	err := dryConn.ReadUpdateDeployment(func(d om.Deployment) error {
		// backupVersions goes from absent (nil) to a slice with a real element.
		d["backupVersions"] = []interface{}{
			map[string]interface{}{"baseUrl": "https://example.com"},
		}
		return nil
	}, zap.S())
	require.NoError(t, err)

	result, err := dryConn.Result()
	require.NoError(t, err)
	assert.NotEmpty(t, result.Added, "addition of a non-empty value must still be reported")
}

// TestDryRunConnection_NoOpWrites verifies that UpdateDeployment, UpdateAutomationConfig,
// and ReadUpdateAgentsLogRotation are no-ops that return nil and leave OM unchanged.
func TestDryRunConnection_NoOpWrites(t *testing.T) {
	dep := baseDryRunDeployment(t)
	mocked := om.NewMockedOmConnection(dep)
	dryConn := om.NewDryRunConnection(mocked)
	log := zap.S()

	_, err := dryConn.UpdateDeployment(dep)
	require.NoError(t, err)

	err = dryConn.UpdateAutomationConfig(nil, log)
	require.NoError(t, err)

	err = dryConn.ReadUpdateAgentsLogRotation(mdbv1.AgentConfig{}, log)
	require.NoError(t, err)

	// Underlying connection must not have been written to.
	assert.Len(t, mocked.GetDeployment()["processes"].([]interface{}), 1)
}
