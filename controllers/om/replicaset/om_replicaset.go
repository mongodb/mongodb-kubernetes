package replicaset

import (
	"strings"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/process"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// BuildFromMongoDBWithReplicas returns a replica set that can be set in the Automation Config
// based on the given MongoDB resource directly without requiring a StatefulSet.
func BuildFromMongoDBWithReplicas(mongoDBImage string, forceEnterprise bool, mdb *mdbv1.MongoDB, replicas int, fcv string, tlsCertPath string, defaultArchitecture architectures.DefaultArchitecture, existingProcessIds map[string]int) om.ReplicaSetWithProcesses {
	legacy := IsLegacyDeployment(existingProcessIds, mdb.Spec.GetExternalMemberProcessNames())
	members := process.CreateMongodProcessesFromMongoDB(mongoDBImage, forceEnterprise, mdb, replicas, fcv, tlsCertPath, defaultArchitecture, legacy)
	replicaSet := om.NewReplicaSet(mdb.GetReplicaSetName(), mdb.Spec.GetMongoDBVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members, mdb.Spec.GetMemberOptions(), existingProcessIds)
	rsWithProcesses.SetHorizons(mdb.Spec.GetHorizonConfig())
	return rsWithProcesses
}

// IsLegacyDeployment Returns true when the AC already has processes under the old naming scheme.
// External member names are excluded, they won't have the prefix but are not K8s-managed.
func IsLegacyDeployment(existingProcessIds map[string]int, externalMembers []string) bool {
	if len(externalMembers) > 0 {
		// If there are external members, we can be sure it is a new deployment.
		// External members can't be added to existing deployments.
		return false
	}

	if len(existingProcessIds) == 0 {
		// If there are no existing members, we can be sure it is a new deployment.
		return false
	}

	// Here it is still possible to have external members which are missing the "k8s/" prefix.
	// This can happen when removing the last external member from an existing deployment.
	foundNewMember := false
	for name := range existingProcessIds {
		if strings.HasPrefix(name, "k8s/") {
			// Found a member with the "k8s/" prefix
			// This means it is a new deployment, even if there are some members without the "k8s/" prefix.
			// There can be only one process without the "k8s/" at this point which is the last external member that was removed
			foundNewMember = true
		}
	}

	// If there is at least one process with the new naming scheme, we consider it a new deployment.
	// If there is a member with the new naming scheme, then this function will return true every subsequent time.
	// Therefore, there shouldn't be a mix of old and new naming schemes.
	// If we got to this point and there are no members with the "k8s/" prefix, then we consider it a legacy deployment.
	// This means it was an existing deployment, never had externalMembers, and all members are using the old naming scheme.
	// Therefore, we continue using the old naming scheme.
	return !foundNewMember
}

// ConvertPodNamesToProcessNames converts bare pod names to k8s/<namespace>/<pod> process names when
// the existing deployment uses the new naming scheme. For legacy deployments the pod names are
// returned unchanged.
func ConvertPodNamesToProcessNames(existingIds map[string]int, externalMembers, podNames []string, namespace string) []string {
	if IsLegacyDeployment(existingIds, externalMembers) {
		return podNames
	}
	processNames := make([]string, len(podNames))
	for i, name := range podNames {
		processNames[i] = process.PodNameToProcessName(name, namespace)
	}
	return processNames
}
