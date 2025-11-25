package deployment

import (
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/replicaset"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// CreateFromReplicaSet builds the replica set for the automation config
// based on the given MongoDB replica set.
// NOTE: This method is only used for testing.
// But we can't move in a *_test file since it is called from tests in
// different packages. And test files are only compiled
// when testing that specific package
// https://github.com/golang/go/issues/10184#issuecomment-84465873
func CreateFromReplicaSet(mongoDBImage string, forceEnterprise bool, rs *mdb.MongoDB) om.Deployment {
	sts := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions(
		func(options *construct.DatabaseStatefulSetOptions) {
			options.PodVars = &env.PodEnvVars{ProjectID: "abcd"}
		},
	), zap.S())
	d := om.NewDeployment()

	lastConfig, err := mdb.GetLastAdditionalMongodConfigByType(nil, mdb.ReplicaSetConfig)
	if err != nil {
		panic(err)
	}

	d.MergeReplicaSet(
		replicaset.BuildFromStatefulSet(mongoDBImage, forceEnterprise, sts, rs.GetSpec(), rs.Status.FeatureCompatibilityVersion, ""),
		rs.Spec.AdditionalMongodConfig.ToMap(),
		lastConfig.ToMap(),
		zap.S(),
	)
	d.AddMonitoringAndBackup(zap.S(), rs.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	d.ConfigureTLS(rs.Spec.GetSecurity(), util.CAFilePathInContainer)
	return d
}
