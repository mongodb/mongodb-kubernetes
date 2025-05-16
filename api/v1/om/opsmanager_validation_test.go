package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

func TestOpsManagerValidation(t *testing.T) {
	type args struct {
		testedOm               *MongoDBOpsManager
		expectedPart           status.Part
		expectedErrorMessage   string
		expectedWarningMessage status.Warning
	}
	tests := map[string]args{
		"Valid KMIP configuration": {
			testedOm: NewOpsManagerBuilderDefault().SetBackup(MongoDBOpsManagerBackup{
				Enabled: true,
				Encryption: &Encryption{
					Kmip: &KmipConfig{
						Server: v1.KmipServerConfig{
							CA:  "kmip-ca",
							URL: "kmip.mongodb.com:5696",
						},
					},
				},
			}).Build(),
			expectedPart: status.None,
		},
		"Valid disabled KMIP configuration": {
			testedOm: NewOpsManagerBuilderDefault().SetBackup(MongoDBOpsManagerBackup{
				Enabled: false,
				Encryption: &Encryption{
					Kmip: &KmipConfig{
						Server: v1.KmipServerConfig{
							URL: "::this::is::a::wrong::address",
						},
					},
				},
			}).Build(),
			expectedPart: status.None,
		},
		"Invalid KMIP configuration with wrong url": {
			testedOm: NewOpsManagerBuilderDefault().SetBackup(MongoDBOpsManagerBackup{
				Enabled: true,
				Encryption: &Encryption{
					Kmip: &KmipConfig{
						Server: v1.KmipServerConfig{
							CA:  "kmip-ca",
							URL: "wrong:::url:::123",
						},
					},
				},
			}).Build(),
			expectedErrorMessage: "kmip url can not be splitted into host and port, see address wrong:::url:::123: too many colons in address",
			expectedPart:         status.OpsManager,
		},
		"Invalid KMIP configuration url and no port": {
			testedOm: NewOpsManagerBuilderDefault().SetBackup(MongoDBOpsManagerBackup{
				Enabled: true,
				Encryption: &Encryption{
					Kmip: &KmipConfig{
						Server: v1.KmipServerConfig{
							CA:  "kmip-ca",
							URL: "localhost",
						},
					},
				},
			}).Build(),
			expectedErrorMessage: "kmip url can not be splitted into host and port, see address localhost: missing port in address",
			expectedPart:         status.OpsManager,
		},
		"Invalid KMIP configuration without CA": {
			testedOm: NewOpsManagerBuilderDefault().SetBackup(MongoDBOpsManagerBackup{
				Enabled: true,
				Encryption: &Encryption{
					Kmip: &KmipConfig{
						Server: v1.KmipServerConfig{
							URL: "kmip.mongodb.com:5696",
						},
					},
				},
			}).Build(),
			expectedErrorMessage: "kmip CA ConfigMap name can not be empty",
			expectedPart:         status.OpsManager,
		},
		"Valid default OpsManager": {
			testedOm:     NewOpsManagerBuilderDefault().Build(),
			expectedPart: status.None,
		},
		"Invalid AppDB connectivity spec": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbConnectivity(mdbv1.MongoDBConnectivity{ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{}}).
				Build(),
			expectedErrorMessage: "connectivity field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB credentials": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbCredentials("invalid").
				Build(),
			expectedErrorMessage: "credentials field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB OpsManager config": {
			testedOm: NewOpsManagerBuilderDefault().
				SetOpsManagerConfig(mdbv1.PrivateCloudConfig{}).
				Build(),
			expectedErrorMessage: "opsManager field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB CloudManager config": {
			testedOm: NewOpsManagerBuilderDefault().
				SetCloudManagerConfig(mdbv1.PrivateCloudConfig{}).
				Build(),
			expectedErrorMessage: "cloudManager field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid S3 Store config": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", MongoDBUserRef: &MongoDBUserRef{Name: "foo"}}).
				Build(),
			expectedErrorMessage: "'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Invalid S3 Store config - missing s3SecretRef": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", S3SecretRef: nil}).
				Build(),
			expectedErrorMessage: "'s3SecretRef' must be specified if not using IRSA (S3 Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Invalid S3 Store config - missing s3SecretRef.Name": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", S3SecretRef: &SecretRef{}}).
				Build(),
			expectedErrorMessage: "'s3SecretRef' must be specified if not using IRSA (S3 Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Valid S3 Store config - no s3SecretRef if irsaEnabled": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", IRSAEnabled: true}).
				Build(),
			expectedPart: status.None,
		},
		"Valid S3 Store config with warning - s3SecretRef present when irsaEnabled": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", S3SecretRef: &SecretRef{}, IRSAEnabled: true}).
				Build(),
			expectedWarningMessage: "'s3SecretRef' must not be specified if using IRSA (S3 Store: test)",
			expectedPart:           status.OpsManager,
		},
		"Invalid S3 OpLog Store config - missing s3SecretRef": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3OplogStoreConfig(S3Config{Name: "test", S3SecretRef: nil}).
				Build(),
			expectedErrorMessage: "'s3SecretRef' must be specified if not using IRSA (S3 OpLog Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Invalid S3 OpLog Store config - missing s3SecretRef.Name": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3OplogStoreConfig(S3Config{Name: "test", S3SecretRef: &SecretRef{}}).
				Build(),
			expectedErrorMessage: "'s3SecretRef' must be specified if not using IRSA (S3 OpLog Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Valid S3 OpLog Store config - no s3SecretRef if irsaEnabled": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3OplogStoreConfig(S3Config{Name: "test", IRSAEnabled: true}).
				Build(),
			expectedPart: status.None,
		},
		"Valid S3 OpLog Store config with warning - s3SecretRef present when irsaEnabled": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3OplogStoreConfig(S3Config{Name: "test", S3SecretRef: &SecretRef{}, IRSAEnabled: true}).
				Build(),
			expectedWarningMessage: "'s3SecretRef' must not be specified if using IRSA (S3 OpLog Store: test)",
			expectedPart:           status.OpsManager,
		},
		"Invalid OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4").
				Build(),
			expectedErrorMessage: "'4.4' is an invalid value for spec.version: Ops Manager Status spec.version 4.4 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid foo OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("foo").
				Build(),
			expectedErrorMessage: "'foo' is an invalid value for spec.version: Ops Manager Status spec.version foo is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4_4.4.0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4_4.4.0").
				Build(),
			expectedErrorMessage: "'4_4.4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4_4.4.0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4.4_4.0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4_4.0").
				Build(),
			expectedErrorMessage: "'4.4_4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4_4.0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4.4.0_0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4.0_0").
				Build(),
			expectedErrorMessage: "'4.4.0_0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4.0_0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Too low AppDB version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbVersion("3.6.12").
				Build(),
			expectedErrorMessage: "the version of Application Database must be >= 4.0",
			expectedPart:         status.AppDb,
		},
		"Valid 4.0.0 OpsManager version": {
			testedOm:     NewOpsManagerBuilderDefault().SetVersion("4.0.0").Build(),
			expectedPart: status.None,
		},
		"Valid 4.2.0-rc1 OpsManager version": {
			testedOm:     NewOpsManagerBuilderDefault().SetVersion("4.2.0-rc1").Build(),
			expectedPart: status.None,
		},
		"Valid 4.5.0-ent OpsManager version": {
			testedOm:     NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").Build(),
			expectedPart: status.None,
		},
		"Single cluster AppDB deployment should have empty clusterSpecList": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetOpsManagerTopology(mdbv1.ClusterTopologySingleCluster).
				SetOpsManagerClusterSpecList([]ClusterSpecOMItem{{ClusterName: "test"}}).
				SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{{ClusterName: "test"}}).
				Build(),
			expectedPart:         status.OpsManager,
			expectedErrorMessage: "Single cluster AppDB deployment should have empty clusterSpecList",
		},
		"Topology 'MultiCluster' must be specified while setting a not empty spec.clusterSpecList": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetOpsManagerTopology(mdbv1.ClusterTopologySingleCluster).
				SetOpsManagerClusterSpecList([]ClusterSpecOMItem{{ClusterName: "test"}}).
				Build(),
			expectedPart:         status.OpsManager,
			expectedErrorMessage: "Topology 'MultiCluster' must be specified while setting a not empty spec.clusterSpecList",
		},
		"Uniform externalDomain can be overwritten multi cluster AppDB": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetAppDBTopology(ClusterTopologyMultiCluster).
				SetAppDbExternalAccess(mdbv1.ExternalAccessConfiguration{
					ExternalDomain: ptr.To("test"),
				}).
				SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
					{
						ClusterName: "cluster1",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test1"),
						},
					},
					{
						ClusterName: "cluster2",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test2"),
						},
					},
					{
						ClusterName: "cluster3",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test3"),
						},
					},
				}).
				Build(),
			expectedPart:         status.None,
			expectedErrorMessage: "",
		},
		"Uniform externalDomain is not allowed for multi cluster AppDB": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetAppDBTopology(ClusterTopologyMultiCluster).
				SetAppDbExternalAccess(mdbv1.ExternalAccessConfiguration{
					ExternalDomain: ptr.To("test"),
				}).
				SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
					{
						ClusterName: "cluster1",
						Members:     1,
					},
					{
						ClusterName: "cluster2",
						Members:     1,
					},
					{
						ClusterName: "cluster3",
						Members:     1,
					},
				}).
				Build(),
			expectedPart: status.AppDb,
			expectedErrorMessage: "Multiple member clusters with the same externalDomain (test) are not allowed. " +
				"Check if all spec.applicationDatabase.clusterSpecList[*].externalAccess.externalDomain fields are defined and are unique.",
		},
		"Multiple member clusters with the same externalDomain are not allowed": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetAppDBTopology(ClusterTopologyMultiCluster).
				SetAppDBClusterSpecList([]mdbv1.ClusterSpecItem{
					{
						ClusterName: "cluster1",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test"),
						},
					},
					{
						ClusterName: "cluster2",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test"),
						},
					},
					{
						ClusterName: "cluster3",
						Members:     1,
						ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
							ExternalDomain: ptr.To("test"),
						},
					},
				}).
				Build(),
			expectedPart: status.AppDb,
			expectedErrorMessage: "Multiple member clusters with the same externalDomain (test) are not allowed. " +
				"Check if all spec.applicationDatabase.clusterSpecList[*].externalAccess.externalDomain fields are defined and are unique.",
		},
	}

	for testName := range tests {
		t.Run(testName, func(t *testing.T) {
			testConfig := tests[testName]
			part, err := testConfig.testedOm.ProcessValidationsOnReconcile()

			if testConfig.expectedErrorMessage != "" {
				assert.NotNil(t, err)
				assert.Equal(t, testConfig.expectedPart, part)
				assert.Equal(t, testConfig.expectedErrorMessage, err.Error())
			} else {
				assert.Nil(t, err)
				assert.Equal(t, status.None, part)
			}

			if testConfig.expectedWarningMessage != "" {
				warnings := testConfig.testedOm.GetStatusWarnings(testConfig.expectedPart)
				assert.Contains(t, warnings, testConfig.expectedWarningMessage)
			}
		})
	}
}

func TestOpsManager_RunValidations_InvalidPreRelease(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("3.5.0-1193-x86_64").SetAppDbVersion("4.4.4-ent").Build()
	version, err := versionutil.StringToSemverVersion(om.Spec.Version)
	assert.NoError(t, err)

	part, err := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.None, part)
	assert.NoError(t, err)
	assert.Equal(t, uint64(3), version.Major)
	assert.Equal(t, uint64(5), version.Minor)
	assert.Equal(t, uint64(0), version.Patch)
}
