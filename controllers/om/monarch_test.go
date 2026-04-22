package om

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetMaintainedMonarchComponents(t *testing.T) {
	d := NewDeployment()

	mc := []MaintainedMonarchComponents{
		{
			ReplicaSetID:       "activeRS",
			ClusterPrefix:      "failoverdemo",
			AWSBucketName:      "my-bucket",
			AWSRegion:          "us-east-1",
			AWSAccessKeyID:     "AKID",
			AWSSecretAccessKey: "SECRET",
			InjectorConfig: InjectorConfig{
				Version: "0.1.1",
				Shards: []InjectorShard{
					{
						ShardID:     "0",
						ReplSetName: "standby-rs",
						Instances: []InjectorInstance{
							{
								ID:                 0,
								Hostname:           "localhost",
								Port:               9995,
								ExternallyManaged:  true,
								HealthAPIEndpoint:  "localhost:8080",
								MonarchAPIEndpoint: "localhost:1122",
							},
						},
					},
				},
			},
		},
	}

	d.SetMaintainedMonarchComponents(mc)

	// Verify the field is present in the deployment map.
	raw, ok := d["maintainedMonarchComponents"]
	require.True(t, ok, "maintainedMonarchComponents should be set")

	// Verify it marshals to the expected JSON shape.
	data, err := json.Marshal(raw)
	require.NoError(t, err)

	var result []map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result, 1)

	assert.Equal(t, "activeRS", result[0]["replicaSetId"])
	assert.Equal(t, "failoverdemo", result[0]["clusterPrefix"])
	assert.Equal(t, "my-bucket", result[0]["awsBucketName"])

	injCfg, ok := result[0]["injectorConfig"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "0.1.1", injCfg["version"])

	shards, ok := injCfg["shards"].([]interface{})
	require.True(t, ok)
	require.Len(t, shards, 1)

	shard := shards[0].(map[string]interface{})
	assert.Equal(t, "standby-rs", shard["replSetName"])

	instances, ok := shard["instances"].([]interface{})
	require.True(t, ok)
	require.Len(t, instances, 1)

	inst := instances[0].(map[string]interface{})
	assert.Equal(t, "localhost", inst["hostname"])
	assert.Equal(t, true, inst["externallyManaged"])
	assert.Equal(t, "localhost:8080", inst["healthApiEndpoint"])
	assert.Equal(t, "localhost:1122", inst["monarchApiEndpoint"])
}
