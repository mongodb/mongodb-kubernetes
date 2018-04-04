package operator

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"go.uber.org/zap"
)

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	log := zap.S().With("replicaSet", s.Name)

	log.Infow("Creating Replica set", "config", s.Spec)

	/*
		TODO this returns some strange empty statefulset...
		if s, _ := c.StatefulSetApi(s.Namespace).Get(s.Name, v1.GetOptions{}); s != nil {
			fmt.Println(s)
			fmt.Printf("Error! Statefulset %s already exists (it was supposed to be removed when MongoDbReplicaSet is removed)\n", s.Name)
			return
		}*/

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(s.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		log.Error("Failed to generate/get agent key: ", err)
		return
	}

	replicaSetObject := buildReplicaSetStatefulSet(s, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(s.Spec.Service, 27017, s.Namespace, true, replicaSetObject)
	if err != nil {
		log.Error("Error trying to create a new statefulset and services for ReplicaSet: ", err)
		return
	}

	if err := c.updateOmDeploymentRs(nil, s); err != nil {
		log.Error("Failed to update OpsManager automation config: ", err)
		return
	}

	log.Info("Created Replica Set!")
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	log := zap.S().With("replicaSet", newRes.Name)

	if err := validateUpdate(oldRes, newRes); err != nil {
		log.Error(err)
		return
	}

	log.Infow("Updating MongoDbReplicaSet", "oldConfig", oldRes.Spec, "newConfig", newRes.Spec.Members)

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(newRes.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		log.Error("Failed to generate/get agent key: ", err)
		return
	}

	replicaSetObject := buildReplicaSetStatefulSet(newRes, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(newRes.Spec.Service, 27017, newRes.Namespace, true, replicaSetObject)
	if err != nil {
		log.Error("Error while updating the StatefulSet: ", err)
		return
	}

	log.Info("Updated statefulset for replicaset")

	if err := c.updateOmDeploymentRs(nil, newRes); err != nil {
		log.Error("Failed to update OpsManager automation config: ", err)
		return
	}

	log.Info("Updated Replica Set!")
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
		return err
	}

	members := createStandalonesForReplica(new.Name, new.Spec.Version, new.Spec.Service, new.Spec.Members)
	deployment.MergeReplicaSet(new.Name, members)

	deployment.AddMonitoring()

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		return err
	}
	return nil
}

func validateUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespaces mismatch")
	}
	return nil
}

func waitUntilAllAgentsAreReady(newRes *mongodb.MongoDbReplicaSet, omConnection *om.OmConnection) bool {
	agentHostnames := make([]string, int(newRes.Spec.Members))
	memberQty := int(newRes.Spec.Members)
	// TODO names of pods must be fetched from Kube api
	serviceName := getOrFormatServiceName(newRes.Spec.Service, newRes.Name)
	for i := 0; i < memberQty; i++ {
		agentHostnames[i] = fmt.Sprintf("%s-%d.%s", newRes.Name, i, serviceName)
	}

	if !om.WaitUntilAgentsHaveRegistered(omConnection, agentHostnames...) {
		return false
	}
	return true
}

// createStandalonesForReplica returns a list of om.Process with specified prefixes
func createStandalonesForReplica(replicaSetName, version string, service *string, memberQty int32) []om.Process {
	collection := make([]om.Process, memberQty)
	qty := int(memberQty)

	sName := getOrFormatServiceName(service, replicaSetName)

	for i := 0; i < qty; i++ {
		// TODO names of pods must be fetched from Kube api
		suffix := fmt.Sprintf("%s.default.svc.cluster.local", sName)
		hostname := fmt.Sprintf("%s-%d.%s", replicaSetName, i, suffix)
		name := fmt.Sprintf("%s_%d", replicaSetName, i)
		member := om.NewProcess(version).
			SetName(name).
			SetHostName(hostname)
		collection[i] = member
	}

	return collection
}
