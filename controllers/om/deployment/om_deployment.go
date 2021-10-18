package deployment

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/replicaset"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

// CreateFromReplicaSet builds the replica set for the automation config
// based on the given MongoDB replica set.
// NOTE: This method is only used for testing.
// But we can't move in a *_test file since it is called from tests in
// different packages. And test files are only compiled
// when testing that specific package
// https://github.com/golang/go/issues/10184#issuecomment-84465873
func CreateFromReplicaSet(rs *mdbv1.MongoDB) om.Deployment {
	sts := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions())
	d := om.NewDeployment()
	d.MergeReplicaSet(
		replicaset.BuildFromStatefulSet(sts, rs.GetSpec()),
		nil,
	)
	d.AddMonitoringAndBackup(zap.S(), rs.Spec.GetTLSConfig().IsEnabled(), util.CAFilePathInContainer)
	d.ConfigureTLS(rs.Spec.GetTLSConfig(), util.CAFilePathInContainer)
	return d
}

// Link returns the deployment link given the baseUrl and groupId.
func Link(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}
