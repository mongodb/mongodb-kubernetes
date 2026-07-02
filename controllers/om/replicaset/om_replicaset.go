package replicaset

import (
	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// BuildFromStatefulSet returns a replica set that can be set in the Automation Config
// based on the given StatefulSet and MongoDB resource.
func BuildFromStatefulSet(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, fcv string, tlsCertPath string, defaultArchitecture architectures.DefaultArchitecture) om.ReplicaSetWithProcesses {
	return BuildFromStatefulSetWithReplicas(mongoDBImage, forceEnterprise, set, dbSpec, int(*set.Spec.Replicas), fcv, tlsCertPath, defaultArchitecture)
}

// BuildFromStatefulSetWithReplicas returns a replica set that can be set in the Automation Config
// based on the given StatefulSet and MongoDB spec. The amount of members is set by the replicas
// parameter.
func BuildFromStatefulSetWithReplicas(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, replicas int, fcv string, tlsCertPath string, defaultArchitecture architectures.DefaultArchitecture) om.ReplicaSetWithProcesses {
	members := process.CreateMongodProcessesWithLimit(mongoDBImage, forceEnterprise, set, dbSpec, replicas, fcv, tlsCertPath, defaultArchitecture)
	replicaSet := om.NewReplicaSet(set.Name, dbSpec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, dbSpec.GetMemberOptions())
	rsWithProcesses.SetHorizons(dbSpec.GetHorizonConfig())
	return rsWithProcesses
}

// BuildFromMongoDBWithReplicas returns a replica set that can be set in the Automation Config
// based on the given MongoDB resource directly without requiring a StatefulSet.
func BuildFromMongoDBWithReplicas(mongoDBImage string, forceEnterprise bool, mdb *mdbv1.MongoDB, replicas int, fcv string, tlsCertPath string, defaultArchitecture architectures.DefaultArchitecture) om.ReplicaSetWithProcesses {
	members := process.CreateMongodProcessesFromMongoDB(mongoDBImage, forceEnterprise, mdb, replicas, fcv, tlsCertPath, defaultArchitecture)
	replicaSet := om.NewReplicaSet(mdb.Name, mdb.Spec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, mdb.Spec.GetMemberOptions())
	rsWithProcesses.SetHorizons(mdb.Spec.GetHorizonConfig())
	return rsWithProcesses
}
