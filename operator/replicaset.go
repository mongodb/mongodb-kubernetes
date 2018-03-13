package operator

import (
	"fmt"
	"time"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"github.com/10gen/ops-manager-kubernetes/om"
)

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	replicaSetObject := buildReplicaSet(s)
	statefulSet, err := c.StatefulSetApi(s.Namespace).Create(replicaSetObject)
	if err != nil {
		fmt.Println("Error trying to create a new ReplicaSet")
		return
	}

	omConnection := NewOpsManagerConnectionFromEnv()

	if !waitUntilAllAgentsAreReady(s, omConnection) {
		fmt.Println("Agents never registered! Not creating replicaset in OM!")
		return
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println(err)
		return
	}

	members := createStandalonesForReplica(s.Spec.HostnamePrefix, s.Spec.Name, s.Spec.Service, s.Spec.Version, *s.Spec.Members)
	deployment.MergeReplicaSet(s.Spec.Name, members)

	deployment.AddMonitoring()

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
	}

	fmt.Printf("Created Replica Set: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	validateUpdate(oldRes, newRes)

	fmt.Printf("Updating MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, *oldRes.Spec.Members, *newRes.Spec.Members)

	// TODO seems it will be great here to log the diff of the objects - can it be made general way through reflection?
	// (to be used by Standalone/ReplicaSet/ShardedCluster

	statefulset, err := c.StatefulSetApi(newRes.Namespace).Update(buildReplicaSet(newRes))
	if err != nil {
		fmt.Printf("Error while updating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	omConfig := GetOpsManagerConfig()
	omConnection := om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)

	if !waitUntilAllAgentsAreReady(newRes, omConnection) {
		fmt.Println("Agents never registered! Not updating replicaset in OM!")
		return
	}

	time.Sleep(4 * time.Second)

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("About to update replicaset with members: %d to %d\n", *oldRes.Spec.Members, *newRes.Spec.Members)

	members := createStandalonesForReplica(newRes.Spec.HostnamePrefix, newRes.Spec.Name, newRes.Spec.Service, newRes.Spec.Version, *newRes.Spec.Members)
	deployment.MergeReplicaSet(newRes.Spec.Name, members)

	deployment.AddMonitoring()

	fmt.Println("We'll update the Deployment in 4 seconds")
	time.Sleep(4 * time.Second)
	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
	}

	fmt.Printf("Updated MongoDbReplicaSet '%s' with %d replicas\n", statefulset.Name, *statefulset.Spec.Replicas)
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	// TODO

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, *s.Spec.Members)
}

func validateUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) {
	if newSpec.Namespace != oldSpec.Namespace {
		panic("Namespaces mismatch")
	}
}

func waitUntilAllAgentsAreReady(newRes *mongodb.MongoDbReplicaSet, omConnection *om.OmConnection) bool {
	agentHostnames := make([]string, int(*newRes.Spec.Members))
	memberQty := int(*newRes.Spec.Members)
	for i := 0; i < memberQty; i++ {
		agentHostnames[i] = fmt.Sprintf("%s-%d.%s", newRes.Spec.HostnamePrefix, i, newRes.Spec.Service)
	}

	if !om.WaitUntilAgentsHaveRegistered(omConnection, agentHostnames...) {
		fmt.Println("Agents never registered! not creating replicaset in OM!")
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
