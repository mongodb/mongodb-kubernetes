package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

func TestOpsManagerValidation(t *testing.T) {
	type args struct {
		testedOm             *MongoDBOpsManager
		expectedPart         status.Part
		expectedError        bool
		expectedErrorMessage string
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
			expectedError: false,
			expectedPart:  status.None,
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
			expectedError: false,
			expectedPart:  status.None,
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
			expectedError: true,
			expectedPart:  status.OpsManager,
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
			expectedError: true,
			expectedPart:  status.OpsManager,
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
			expectedError: true,
			expectedPart:  status.OpsManager,
		},
		"Valid default OpsManager": {
			testedOm:      NewOpsManagerBuilderDefault().Build(),
			expectedError: false,
			expectedPart:  status.None,
		},
		"Invalid AppDB connectivity spec": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbConnectivity(mdbv1.MongoDBConnectivity{ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{}}).
				Build(),
			expectedError:        true,
			expectedErrorMessage: "connectivity field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB credentials": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbCredentials("invalid").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "credentials field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB OpsManager config": {
			testedOm: NewOpsManagerBuilderDefault().
				SetOpsManagerConfig(mdbv1.PrivateCloudConfig{}).
				Build(),
			expectedError:        true,
			expectedErrorMessage: "opsManager field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid AppDB CloudManager config": {
			testedOm: NewOpsManagerBuilderDefault().
				SetCloudManagerConfig(mdbv1.PrivateCloudConfig{}).
				Build(),
			expectedError:        true,
			expectedErrorMessage: "cloudManager field is not configurable for application databases",
			expectedPart:         status.AppDb,
		},
		"Invalid S3 Store config": {
			testedOm: NewOpsManagerBuilderDefault().
				AddS3SnapshotStore(S3Config{Name: "test", MongoDBUserRef: &MongoDBUserRef{Name: "foo"}}).
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: test)",
			expectedPart:         status.OpsManager,
		},
		"Invalid OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'4.4' is an invalid value for spec.version: Ops Manager Status spec.version 4.4 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid foo OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("foo").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'foo' is an invalid value for spec.version: Ops Manager Status spec.version foo is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4_4.4.0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4_4.4.0").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'4_4.4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4_4.4.0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4.4_4.0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4_4.0").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'4.4_4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4_4.0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Invalid 4.4.0_0 OpsManager version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetVersion("4.4.0_0").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "'4.4.0_0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4.0_0 is invalid",
			expectedPart:         status.OpsManager,
		},
		"Too low AppDB version": {
			testedOm: NewOpsManagerBuilderDefault().
				SetAppDbVersion("3.6.12").
				Build(),
			expectedError:        true,
			expectedErrorMessage: "the version of Application Database must be >= 4.0",
			expectedPart:         status.AppDb,
		},
		"Valid 4.0.0 OpsManager version": {
			testedOm:      NewOpsManagerBuilderDefault().SetVersion("4.0.0").Build(),
			expectedError: false,
			expectedPart:  status.None,
		},
		"Valid 4.2.0-rc1 OpsManager version": {
			testedOm:      NewOpsManagerBuilderDefault().SetVersion("4.2.0-rc1").Build(),
			expectedError: false,
			expectedPart:  status.None,
		},
		"Valid 4.5.0-ent OpsManager version": {
			testedOm:      NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").Build(),
			expectedError: false,
			expectedPart:  status.None,
		},
		"Single cluster AppDB deployment should have empty clusterSpecList": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetOpsManagerTopology(mdbv1.ClusterTopologySingleCluster).
				SetOpsManagerClusterSpecList([]ClusterSpecOMItem{{ClusterName: "test"}}).
				SetAppDBClusterSpecList(mdbv1.ClusterSpecList{{ClusterName: "test"}}).
				Build(),
			expectedError:        true,
			expectedPart:         status.OpsManager,
			expectedErrorMessage: "Single cluster AppDB deployment should have empty clusterSpecList",
		},
		"Topology 'MultiCluster' must be specified while setting a not empty spec.clusterSpecList": {
			testedOm: NewOpsManagerBuilderDefault().SetVersion("4.5.0-ent").
				SetOpsManagerTopology(mdbv1.ClusterTopologySingleCluster).
				SetOpsManagerClusterSpecList([]ClusterSpecOMItem{{ClusterName: "test"}}).
				Build(),
			expectedError:        true,
			expectedPart:         status.OpsManager,
			expectedErrorMessage: "Topology 'MultiCluster' must be specified while setting a not empty spec.clusterSpecList",
		},
	}
	for testName := range tests {
		t.Run(testName, func(t *testing.T) {
			testConfig := tests[testName]
			part, err := testConfig.testedOm.ProcessValidationsOnReconcile()
			assert.Equal(t, testConfig.expectedError, err != nil)
			assert.Equal(t, testConfig.expectedPart, part)
			if len(testConfig.expectedErrorMessage) != 0 {
				assert.Equal(t, testConfig.expectedErrorMessage, err.Error())
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
