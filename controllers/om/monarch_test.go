package om

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

func TestSetMaintainedMonarchComponents(t *testing.T) {
	d := NewDeployment()

	mc := []MaintainedMonarchComponents{
		{
			ReplicaSetID:  "activeRS",
			ClusterPrefix: "failoverdemo",
			AwsConfig: &AwsStorageConfig{
				AWSBucketName:      "my-bucket",
				AWSRegion:          "us-east-1",
				AWSAccessKeyID:     "AKID",
				AWSSecretAccessKey: "SECRET",
			},
			InjectorConfig: &InjectorConfig{
				Version: "0.1.1",
				Shards: []MonarchShard{
					{
						ShardID:     "0",
						ReplSetName: "standby-rs",
						Instances: []MonarchInstance{
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

	awsCfg, ok := result[0]["awsConfig"].(map[string]interface{})
	require.True(t, ok, "awsConfig should be a nested object")
	assert.Equal(t, "my-bucket", awsCfg["awsBucketName"])

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
				Role:  role,
				Image: "quay.io/mongodb/monarch:0.1.1",
				S3: mdbv1.MonarchS3Config{
					Bucket:    "my-bucket",
					Region:    "us-east-1",
					Prefix:    "failoverdemo",
					Endpoint:  "http://minio:9000",
					PathStyle: true,
				},
			},
		},
	}
}

func TestBuildMaintainedMonarchComponents_Standby(t *testing.T) {
	mdb := newMonarchMDB(mdbv1.MonarchRoleStandby)
	serviceDNS := "my-rs-monarch-injector-svc.ns.svc.cluster.local"
	mongoURI := "mongodb://my-rs-0.my-rs-svc.ns.svc.cluster.local:27017"

	result, err := BuildMaintainedMonarchComponents(mdb, "standby-rs", "AKID", "SECRET", serviceDNS, mongoURI)
	require.NoError(t, err)
	require.Len(t, result, 1)

	mc := result[0]
	// ReplicaSetID is the local RS name. DR pair linkage is via shared ClusterPrefix.
	assert.Equal(t, "standby-rs", mc.ReplicaSetID)
	assert.Equal(t, "failoverdemo", mc.ClusterPrefix)
	require.NotNil(t, mc.AwsConfig)
	assert.Equal(t, "my-bucket", mc.AwsConfig.AWSBucketName)
	assert.Equal(t, "us-east-1", mc.AwsConfig.AWSRegion)
	assert.Equal(t, "AKID", mc.AwsConfig.AWSAccessKeyID)
	assert.Equal(t, "SECRET", mc.AwsConfig.AWSSecretAccessKey)
	assert.Equal(t, "http://minio:9000", mc.AwsConfig.S3BucketEndpoint)
	assert.True(t, mc.AwsConfig.S3PathStyleAccess)

	require.NotNil(t, mc.InjectorConfig)
	assert.Equal(t, "0.1.1", mc.InjectorConfig.Version)
	require.Len(t, mc.InjectorConfig.Shards, 1)

	shard := mc.InjectorConfig.Shards[0]
	assert.Equal(t, "0", shard.ShardID)
	assert.Equal(t, "standby-rs", shard.ReplSetName)
	// Single instance pointing at the K8s Service DNS.
	// ExternallyManaged=true + MonarchApiEndpoint set: agent routes directly to the
	// service and skips hostname locality checks (see mms-automation injectorclient.go).
	require.Len(t, shard.Instances, 1)

	inst := shard.Instances[0]
	assert.Equal(t, 0, inst.ID)
	assert.Equal(t, serviceDNS, inst.Hostname)
	assert.Equal(t, 9995, inst.Port)
	assert.True(t, inst.ExternallyManaged)
	assert.Equal(t, serviceDNS+":8080", inst.HealthAPIEndpoint)
	assert.Equal(t, serviceDNS+":1122", inst.MonarchAPIEndpoint)
	assert.Equal(t, mongoURI, inst.SrcURI)

	// MongodURIs / InjectorHosts are required by the injector binary's
	// ValidateStandbyConfig. mongodURIs has one entry per RS member; the
	// injectorHosts has a single entry — the K8s Service DNS — because
	// OM's StandbyModificationsSvc adds one injector RS member per AC instance.
	assert.Equal(t, []string{
		"mongodb://my-rs-0.my-rs-svc.ns.svc.cluster.local:27017/",
		"mongodb://my-rs-1.my-rs-svc.ns.svc.cluster.local:27017/",
		"mongodb://my-rs-2.my-rs-svc.ns.svc.cluster.local:27017/",
	}, inst.MongodURIs)
	assert.Equal(t, []string{serviceDNS + ":9995"}, inst.InjectorHosts)
}

func TestBuildMaintainedMonarchComponents_Active(t *testing.T) {
	mdb := newMonarchMDB(mdbv1.MonarchRoleActive)
	serviceDNS := "my-rs-monarch-shipper-svc.ns.svc.cluster.local"
	mongoURI := "mongodb://my-rs-0.my-rs-svc.ns.svc.cluster.local:27017"

	result, err := BuildMaintainedMonarchComponents(mdb, "active-rs", "AKID", "SECRET", serviceDNS, mongoURI)
	require.NoError(t, err)
	require.Len(t, result, 1)

	mc := result[0]
	assert.Equal(t, "active-rs", mc.ReplicaSetID)
	require.NotNil(t, mc.ShipperConfig)
	assert.Len(t, mc.ShipperConfig.Shards, 1)
	// Mode and BackupMongoNodeURI are per-instance fields.
	require.Len(t, mc.ShipperConfig.Shards[0].Instances, 1)
	inst := mc.ShipperConfig.Shards[0].Instances[0]
	assert.Equal(t, monarchShipperMode, inst.Mode)
	assert.Equal(t, mongoURI, inst.BackupMongoNodeURI)
	// Active (shipper) path doesn't populate the injector-only fields.
	assert.Empty(t, inst.MongodURIs)
	assert.Empty(t, inst.InjectorHosts)
	// Active clusters have no injector config.
	assert.Nil(t, mc.InjectorConfig)
}

func TestBuildMaintainedMonarchComponents_NilMonarch(t *testing.T) {
	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "ns"},
		Spec:       mdbv1.MongoDbSpec{Members: 3},
	}

	_, err := BuildMaintainedMonarchComponents(mdb, "rs", "AKID", "SECRET", "", "")
	require.Error(t, err)
}
