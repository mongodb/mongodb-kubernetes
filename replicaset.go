package main

import (
	"fmt"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	om "github.com/10gen/ops-manager-kubernetes/om"
)

// BuildReplicaSet will return a StatefulSet definition, built on top of Pods.
func BuildReplicaSet(obj *mongodb.MongoDbReplicaSet) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        obj.Spec.Service,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.HostnamePrefix,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    MongoDbReplicaSet,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: obj.Spec.Service,
			Replicas:    obj.Spec.Members,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: BaseContainer(obj.Spec.HostnamePrefix),
			},
		},
	}
}

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	replicaSetObject := BuildReplicaSet(s)
	statefulSet, err := c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Create(replicaSetObject)
	if err != nil {
		fmt.Println("Error trying to create a new ReplicaSet")
		return
	}

	omConfig := GetOpsManagerConfig()
	omConnection := om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)

	agentsOk := false
	for count := 0; count < 3; count++ {
		time.Sleep(3 * time.Second)

		path := fmt.Sprintf(OpsManagerAgentsResource, omConfig.GroupId)
		agentResponse, err := omConnection.Get(path)
		if err != nil {
			fmt.Println("Unable to read from OM API, waiting...")
			fmt.Println(err)
			continue
		}

		fmt.Println("Checking if the agent have registered yet")
		fmt.Printf("Checked %s against response\n", s.Spec.HostnamePrefix)
		if CheckAgentExists(s.Spec.HostnamePrefix, agentResponse) {
			fmt.Println("Found agents have already registered!")
			agentsOk = true
			break
		}
		fmt.Println("Agents have not registered with OM, waiting...")
	}
	if !agentsOk {
		fmt.Println("Agents never registered! not creating standalone in OM!")
		return
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println(err)
		return
	}

	members := CreateStandalonesForReplica(s.Spec.HostnamePrefix, s.Spec.Name, s.Spec.Service, s.Spec.Version, *s.Spec.Members)
	deployment.MergeReplicaSet(s.Spec.Name, members)

	deployment.AddMonitoring()

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
	}

	fmt.Printf("Created Standalone: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Updated MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, *oldRes.Spec.Members, *newRes.Spec.Members)

	if newRes.Namespace != oldRes.Namespace {
		panic("Namespaces mismatch")
	}
	statefulset, err := c.context.Clientset.AppsV1().StatefulSets(newRes.Namespace).Update(BuildReplicaSet(newRes))
	if err != nil {
		fmt.Printf("Error while creating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	omConfig := GetOpsManagerConfig()
	omConnection := om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)

	action := "Scaling UP"
	if newRes.Spec.Members != oldRes.Spec.Members {
		// Scaling!
		if *newRes.Spec.Members < *oldRes.Spec.Members {
			fmt.Println("Scale Down!")
			action = "Scaling DOWN"
			// Scaling Down.
			// First, contact the API and remove the hosts from the RS
			// Then remove the hosts
		} else if *newRes.Spec.Members > *oldRes.Spec.Members {
			fmt.Println("Scale UP!")
			// Scaling UP.
			// First, create the hosts and then add them to replica
		}
	}

	agentHostnames := make([]string, int(*newRes.Spec.Members))
	memberQty := int(*newRes.Spec.Members)
	for i := 0; i < memberQty; i++ {
		agentHostnames[i] = fmt.Sprintf("%s-%d.%s", newRes.Spec.HostnamePrefix, i, newRes.Spec.Service)
	}

	if !WaitUntilAgentsHaveRegistered(omConnection, agentHostnames) {
		fmt.Println("Agents never registered! not creating replicaset in OM!")
		return
	}

	fmt.Printf("Waiting 4 seconds before %s\n", action)
	time.Sleep(4 * time.Second)

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println(err)
		return
	} else {
		fmt.Println("Got something from ReadDeployment")
	}

	fmt.Printf("About to update replicaset with members: %d to %d\n", *oldRes.Spec.Members, *newRes.Spec.Members)

	members := CreateStandalonesForReplica(newRes.Spec.HostnamePrefix, newRes.Spec.Name, newRes.Spec.Service, newRes.Spec.Version, *newRes.Spec.Members)
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

// CreateStandaloneForReplica returns a list of om.Standalones
func CreateStandalonesForReplica(hostnamePrefix, replicaSetName, service, version string, memberQty int32) []om.Process {
	collection := make([]om.Process, memberQty)
	qty := int(memberQty)

	for i := 0; i < qty; i++ {
		suffix := fmt.Sprintf("%s.default.svc.cluster.local", service)
		hostname := fmt.Sprintf("%s-%d.%s", hostnamePrefix, i, suffix)
		name := fmt.Sprintf("%s_%d", replicaSetName, i)
		member := om.NewProcess(version).
			SetName(name).
			SetHostName(hostname).
			SetDbPath("/data").
			SetLogPath("/data/mongodb.log")
		collection[i] = member
	}

	return collection
}