package deployment

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/replicaset"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"go.uber.org/zap"
)

// CreateFromReplicaSet builds the replica set for the automation config
// based on the given MongoDB replica set.
func CreateFromReplicaSet(rs *mdbv1.MongoDB) om.Deployment {
	sts := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions())
	d := om.NewDeployment()
	d.MergeReplicaSet(
		replicaset.BuildFromStatefulSet(sts, rs),
		nil,
	)
	d.AddMonitoringAndBackup(zap.S(), rs.Spec.GetTLSConfig().IsEnabled())
	d.ConfigureTLS(rs.Spec.GetTLSConfig())
	return d
}

// Link returns the deployment link given the baseUrl and groupId.
func Link(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}
