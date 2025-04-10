package controlledfeature

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestBuildFeatureControlsByMdb_MongodbParams(t *testing.T) {
	t.Run("Feature controls for replica set additional params", func(t *testing.T) {
		config := mdbv1.NewAdditionalMongodConfig("storage.journal.enabled", true).AddOption("storage.indexBuildRetry", true)
		rs := mdbv1.NewReplicaSetBuilder().SetAdditionalConfig(config).Build()
		controlledFeature := buildFeatureControlsByMdb(*rs)

		expectedControlledFeature := &ControlledFeature{
			ManagementSystem: ManagementSystem{
				Name:    util.OperatorName,
				Version: util.OperatorVersion,
			},
			Policies: []Policy{
				{PolicyType: ExternallyManaged, DisabledParams: make([]string, 0)},
				{
					PolicyType:     DisableMongodConfig,
					DisabledParams: []string{"storage.indexBuildRetry", "storage.journal.enabled"},
				},
				{PolicyType: DisableMongodVersion},
			},
		}
		assert.Equal(t, expectedControlledFeature, controlledFeature)
	})
	t.Run("Feature controls for sharded cluster additional params", func(t *testing.T) {
		shardConfig := mdbv1.NewAdditionalMongodConfig("storage.journal.enabled", true).AddOption("storage.indexBuildRetry", true)
		mongosConfig := mdbv1.NewAdditionalMongodConfig("systemLog.verbosity", 2)
		configSrvConfig := mdbv1.NewAdditionalMongodConfig("systemLog.verbosity", 5).AddOption("systemLog.traceAllExceptions", true)

		rs := mdbv1.NewClusterBuilder().
			SetShardAdditionalConfig(shardConfig).
			SetMongosAdditionalConfig(mongosConfig).
			SetConfigSrvAdditionalConfig(configSrvConfig).
			Build()
		controlledFeature := buildFeatureControlsByMdb(*rs)

		expectedControlledFeature := &ControlledFeature{
			ManagementSystem: ManagementSystem{
				Name:    util.OperatorName,
				Version: util.OperatorVersion,
			},
			Policies: []Policy{
				{PolicyType: ExternallyManaged, DisabledParams: make([]string, 0)},
				{
					PolicyType: DisableMongodConfig,
					// The options have been deduplicated and contain the list of all options for each sharded cluster member
					DisabledParams: []string{"storage.indexBuildRetry", "storage.journal.enabled", "systemLog.traceAllExceptions", "systemLog.verbosity"},
				},
				{PolicyType: DisableMongodVersion},
			},
		}
		assert.Equal(t, expectedControlledFeature, controlledFeature)
	})
}
