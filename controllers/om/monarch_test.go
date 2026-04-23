package om

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
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

func newMonarchMDB(role mdbv1.MonarchRole) *mdbv1.MongoDB {
	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-rs",
			Namespace: "ns",
		},
		Spec: mdbv1.MongoDbSpec{
			Members: 3,
			Monarch: &mdbv1.MonarchSpec{
				Role:               role,
				S3BucketName:       "my-bucket",
				AWSRegion:          "us-east-1",
				ClusterPrefix:      "failoverdemo",
				S3BucketEndpoint:   "http://minio:9000",
				S3PathStyleAccess:  true,
				ShipperVersion:     "0.1.1",
				InjectorVersion:    "0.1.1",
				ActiveReplicaSetId: "active-rs",
			},
		},
	}
}

func TestBuildMaintainedMonarchComponents_Standby(t *testing.T) {
	mdb := newMonarchMDB(mdbv1.MonarchRoleStandby)
	dnsNames := []string{
		"my-rs-monarch-0-svc.ns.svc.cluster.local",
		"my-rs-monarch-1-svc.ns.svc.cluster.local",
		"my-rs-monarch-2-svc.ns.svc.cluster.local",
	}

	result, err := BuildMaintainedMonarchComponents(mdb, "standby-rs", "AKID", "SECRET", dnsNames)
	require.NoError(t, err)
	require.Len(t, result, 1)

	mc := result[0]
	// Standby uses the ActiveReplicaSetId as the ReplicaSetID.
	assert.Equal(t, "active-rs", mc.ReplicaSetID)
	assert.Equal(t, "failoverdemo", mc.ClusterPrefix)
	assert.Equal(t, "my-bucket", mc.AWSBucketName)
	assert.Equal(t, "us-east-1", mc.AWSRegion)
	assert.Equal(t, "AKID", mc.AWSAccessKeyID)
	assert.Equal(t, "SECRET", mc.AWSSecretAccessKey)
	assert.Equal(t, "http://minio:9000", mc.S3BucketEndpoint)
	assert.True(t, mc.S3PathStyleAccess)

	assert.Equal(t, "0.1.1", mc.InjectorConfig.Version)
	require.Len(t, mc.InjectorConfig.Shards, 1)

	shard := mc.InjectorConfig.Shards[0]
	assert.Equal(t, "0", shard.ShardID)
	assert.Equal(t, "standby-rs", shard.ReplSetName)
	require.Len(t, shard.Instances, 3)

	for i, inst := range shard.Instances {
		assert.Equal(t, i, inst.ID)
		assert.Equal(t, dnsNames[i], inst.Hostname)
		assert.Equal(t, 9995, inst.Port)
		assert.True(t, inst.ExternallyManaged)
		assert.Equal(t, dnsNames[i]+":8080", inst.HealthAPIEndpoint)
		assert.Equal(t, dnsNames[i]+":1122", inst.MonarchAPIEndpoint)
	}
}

func TestBuildMaintainedMonarchComponents_Active(t *testing.T) {
	mdb := newMonarchMDB(mdbv1.MonarchRoleActive)
	dnsNames := []string{
		"my-rs-monarch-0-svc.ns.svc.cluster.local",
	}

	result, err := BuildMaintainedMonarchComponents(mdb, "active-rs", "AKID", "SECRET", dnsNames)
	require.NoError(t, err)
	require.Len(t, result, 1)

	mc := result[0]
	assert.Equal(t, "active-rs", mc.ReplicaSetID)
	assert.Equal(t, "0.1.1", mc.InjectorConfig.Version)
	// Active clusters have no injector instances.
	assert.Empty(t, mc.InjectorConfig.Shards)
}

func TestBuildMaintainedMonarchComponents_NilMonarch(t *testing.T) {
	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "ns"},
		Spec:       mdbv1.MongoDbSpec{Members: 3},
	}

	_, err := BuildMaintainedMonarchComponents(mdb, "rs", "AKID", "SECRET", nil)
	require.Error(t, err)
}
