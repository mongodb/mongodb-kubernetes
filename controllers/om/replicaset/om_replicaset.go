package replicaset

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/process"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/scale"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	appsv1 "k8s.io/api/apps/v1"
)

// BuildFromStatefulSetWithReplicas returns a replica set that can be set in the Automation Config
// based on the given StatefulSet and MongoDB spec. The amount of members is set by the replicas
// parameter.
func BuildFromStatefulSetWithReplicas(set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, replicas int) om.ReplicaSetWithProcesses {
	members := process.CreateMongodProcessesWithLimit(set, dbSpec, replicas)
	replicaSet := om.NewReplicaSet(set.Name, dbSpec.GetMongoDBVersion(nil))
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, dbSpec.GetMemberOptions())
	rsWithProcesses.SetHorizons(dbSpec.GetHorizonConfig())
	return rsWithProcesses
}

// PrepareScaleDownFromMap performs additional steps necessary to make sure removed members are not primary (so no
// election happens and replica set is available) (see
// https://jira.mongodb.org/browse/HELP-3818?focusedCommentId=1548348 for more details)
// Note, that we are skipping setting nodes as "disabled" (but the code is commented to be able to revert this if
// needed)
func PrepareScaleDownFromMap(omClient om.Connection, rsMembers map[string][]string, log *zap.SugaredLogger) error {
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

		if err := om.WaitForReadyState(omClient, processes, false, log); err != nil {
			return err
		}

		log.Debugw("Marked replica set members as non-voting", "replica set with members", rsMembers)
	}

	// TODO practice shows that automation agents can get stuck on setting db to "disabled" also it seems that this process
	// works correctly without explicit disabling - feel free to remove this code after some time when it is clear
	// that everything works correctly without disabling

	// Stage 2. Set disabled to true
	//err = omClient.ReadUpdateDeployment(
	//	func(d om.Deployment) error {
	//		d.DisableProcesses(allProcesses)
	//		return nil
	//	},
	//)
	//
	//if err != nil {
	//	return errors.New(fmt.Sprintf("Unable to set disabled to true, hosts: %v, err: %w", allProcesses, err))
	//}
	//log.Debugw("Disabled processes", "processes", allProcesses)

	log.Infow("Performed some preliminary steps to support scale down", "hosts", processes)

	return nil
}

func PrepareScaleDownFromStatefulSet(omClient om.Connection, statefulSet appsv1.StatefulSet, rs *mdbv1.MongoDB, log *zap.SugaredLogger) error {
	_, podNames := dns.GetDnsForStatefulSetReplicasSpecified(statefulSet, rs.Spec.GetClusterDomain(), rs.Status.Members, nil)
	podNames = podNames[scale.ReplicasThisReconciliation(rs):rs.Status.Members]

	if len(podNames) != 1 {
		return xerrors.Errorf("dev error: the number of members being scaled down was > 1, scaling more than one member at a time is not possible! %s", podNames)
	}

	log.Debugw("Setting votes to 0 for members", "members", podNames)
	return PrepareScaleDownFromMap(omClient, map[string][]string{rs.Name: podNames}, log)
}
