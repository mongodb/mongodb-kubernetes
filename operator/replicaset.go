package operator

import (
	"fmt"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"github.com/10gen/ops-manager-kubernetes/om"
	"errors"
)

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Creating Replica set %s with the following config: %+v\n", s.Name, s.Spec)

/*
	TODO this returns some strange empty statefulset...
	if s, _ := c.StatefulSetApi(s.Namespace).Get(s.Name, v1.GetOptions{}); s != nil {
		fmt.Println(s)
		fmt.Printf("Error! Statefulset %s already exists (it was supposed to be removed when MongoDbReplicaSet is removed)\n", s.Name)
		return
	}*/


	agentKeySecretName, err := c.EnsureAgentKeySecretExists(s.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		fmt.Println("Failed to generate/get agent key")
		fmt.Println(err)
		return;
	}

	replicaSetObject := buildReplicaSetStatefulSet(s, agentKeySecretName)
	statefulSet, err := c.StatefulSetApi(s.Namespace).Create(replicaSetObject)
	if err != nil {
		fmt.Println("Error trying to create a new ReplicaSet")
		fmt.Println(err)
		return
	}
	fmt.Println("Created statefulset for replicaset")

	if err := c.updateOmDeploymentRs(nil, s); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Created Replica Set: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	validateUpdate(oldRes, newRes)

	fmt.Printf("Updating MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, oldRes.Spec.Members, newRes.Spec.Members)

	// TODO seems it will be great here to log the diff of the objects - can it be made general way through reflection?
	// (to be used by Standalone/ReplicaSet/ShardedCluster

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(newRes.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		fmt.Println("Failed to generate/get agent key")
		fmt.Println(err)
		return;
	}

	replicaSetObject := buildReplicaSetStatefulSet(newRes, agentKeySecretName)
	statefulset, err := c.StatefulSetApi(newRes.Namespace).Update(replicaSetObject)
	if err != nil {
		fmt.Printf("Error while updating the StatefulSet\n")
		fmt.Println(err)
		return
	}
	fmt.Println("Updated statefulset for replicaset")

	if err := c.updateOmDeploymentRs(nil, newRes); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Updated MongoDbReplicaSet '%s' with %d replicas\n", statefulset.Name, *statefulset.Spec.Replicas)
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	// TODO

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, s.Spec.Members)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *MongoDbController) updateOmDeploymentRs(old, new *mongodb.MongoDbReplicaSet) error {
	omConnection := NewOpsManagerConnectionFromEnv()

	if !waitUntilAllAgentsAreReady(new, omConnection) {
		return errors.New("Some of the agents failed to register! Not creating replicaset in OM!")
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println(err)
		return err
	}

	members := createStandalonesForReplica(new.Spec.HostnamePrefix, new.Spec.Name, new.Spec.Service, new.Spec.Version, new.Spec.Members)
	deployment.MergeReplicaSet(new.Spec.Name, members)

	deployment.AddMonitoring()

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
		return err
	}
	return nil
}

func validateUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) {
	if newSpec.Namespace != oldSpec.Namespace {
		panic("Namespaces mismatch")
	}
}

func waitUntilAllAgentsAreReady(newRes *mongodb.MongoDbReplicaSet, omConnection *om.OmConnection) bool {
	agentHostnames := make([]string, int(newRes.Spec.Members))
	memberQty := int(newRes.Spec.Members)
	for i := 0; i < memberQty; i++ {
		agentHostnames[i] = fmt.Sprintf("%s-%d.%s", newRes.Spec.HostnamePrefix, i, newRes.Spec.Service)
	}

	if !om.WaitUntilAgentsHaveRegistered(omConnection, agentHostnames...) {
		fmt.Println("(A) Agents never registered! not creating replicaset in OM!")
		return false
	}
	return true
}

// createStandalonesForReplica returns a list of om.Process with specified prefixes
func createStandalonesForReplica(hostnamePrefix, replicaSetName, service, version string, memberQty int32) []om.Process {
	collection := make([]om.Process, memberQty)
	qty := int(memberQty)

	for i := 0; i < qty; i++ {
		suffix := fmt.Sprintf("%s.default.svc.cluster.local", service)
		hostname := fmt.Sprintf("%s-%d.%s", hostnamePrefix, i, suffix)
		name := fmt.Sprintf("%s_%d", replicaSetName, i)
		member := om.NewProcess(version).
			SetName(name).
			SetHostName(hostname)
		collection[i] = member
	}

	return collection
}
