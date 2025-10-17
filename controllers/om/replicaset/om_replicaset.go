package replicaset

import (
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/scale"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
)

// BuildFromStatefulSet returns a replica set that can be set in the Automation Config
// based on the given StatefulSet and MongoDB resource.
func BuildFromStatefulSet(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, fcv string, tlsCertPath string) om.ReplicaSetWithProcesses {
	return BuildFromStatefulSetWithReplicas(mongoDBImage, forceEnterprise, set, dbSpec, int(*set.Spec.Replicas), fcv, tlsCertPath)
}

// BuildFromStatefulSetWithReplicas returns a replica set that can be set in the Automation Config
// based on the given StatefulSet and MongoDB spec. The amount of members is set by the replicas
// parameter.
func BuildFromStatefulSetWithReplicas(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, replicas int, fcv string, tlsCertPath string) om.ReplicaSetWithProcesses {
	members := process.CreateMongodProcessesWithLimit(mongoDBImage, forceEnterprise, set, dbSpec, replicas, fcv, tlsCertPath)
	replicaSet := om.NewReplicaSet(set.Name, dbSpec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, dbSpec.GetMemberOptions())
	rsWithProcesses.SetHorizons(dbSpec.GetHorizonConfig())
	return rsWithProcesses
}

// BuildFromMongoDBWithReplicas returns a replica set that can be set in the Automation Config
// based on the given MongoDB resource directly without requiring a StatefulSet.
func BuildFromMongoDBWithReplicas(mongoDBImage string, forceEnterprise bool, mdb *mdbv1.MongoDB, replicas int, fcv string, tlsCertPath string) om.ReplicaSetWithProcesses {
	members := process.CreateMongodProcessesFromMongoDB(mongoDBImage, forceEnterprise, mdb, replicas, fcv, tlsCertPath)
	replicaSet := om.NewReplicaSet(mdb.Name, mdb.Spec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, mdb.Spec.GetMemberOptions())
	rsWithProcesses.SetHorizons(mdb.Spec.GetHorizonConfig())
	return rsWithProcesses
}

// PrepareScaleDownFromMap performs additional steps necessary to make sure removed members are not primary (so no
// election happens and replica set is available) (see
// https://jira.mongodb.org/browse/HELP-3818?focusedCommentId=1548348 for more details)
// Note, that we are skipping setting nodes as "disabled" (but the code is commented to be able to revert this if
// needed)
func PrepareScaleDownFromMap(omClient om.Connection, rsMembers map[string][]string, processesToWaitForGoalState []string, log *zap.SugaredLogger) error {
	processes := make([]string, 0)
	for _, v := range rsMembers {
		processes = append(processes, v...)
	}

	// Stage 1. Set Votes and Priority to 0
	if len(rsMembers) > 0 && len(processes) > 0 {
		err := omClient.ReadUpdateDeployment(
			func(d om.Deployment) error {
				for k, v := range rsMembers {
					if err := d.MarkRsMembersUnvoted(k, v); err != nil {
						log.Errorf("Problems scaling down some replica sets (were they changed in Ops Manager directly?): %s", err)
					}
				}
				return nil
			},
			log,
		)
		if err != nil {
			return xerrors.Errorf("unable to set votes, priority to 0 in Ops Manager, hosts: %v, err: %w", processes, err)
		}

		if err := om.WaitForReadyState(omClient, processesToWaitForGoalState, false, log); err != nil {
			return err
		}

		log.Debugw("Marked replica set members as non-voting", "replica set with members", rsMembers)
	}

	log.Infow("Performed some preliminary steps to support scale down", "hosts", processes)

	return nil
}

func PrepareScaleDownFromMongoDB(omClient om.Connection, rs *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := dns.GetDNSNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.GetClusterDomain(), rs.Status.Members, rs.Spec.DbCommonSpec.GetExternalDomain())
	podNames = podNames[scale.ReplicasThisReconciliation(rs):rs.Status.Members]

	if len(podNames) != 1 {
		return xerrors.Errorf("dev error: the number of members being scaled down was > 1, scaling more than one member at a time is not possible! %s", podNames)
	}

	log.Debugw("Setting votes to 0 for members", "members", podNames)
	return PrepareScaleDownFromMap(omClient, map[string][]string{rs.Name: podNames}, podNames, log)
}
