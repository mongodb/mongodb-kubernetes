package main

import (
	"fmt"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"com.tengen/cm/config"
	"com.tengen/cm/core"
	"com.tengen/cm/hosts"
	om "github.com/10gen/ops-manager-kubernetes/om"
)

// BuildReplicaSet will return a StatefulSet definition, built on top of Pods.
func BuildReplicaSet(obj *mongodb.MongoDbReplicaSet) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        "my-service",
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
			ServiceName: "my-service",
			Replicas:    obj.Spec.Members,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: BaseContainer(),
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

	// TODO: This is to fix the error with UpperCase attribute names
	deployment.MongoDbVersions = make([]*config.MongoDbVersionConfig, 1)
	deployment.MongoDbVersions[0] = &config.MongoDbVersionConfig{Name: s.Spec.Version}
	// END

	members := CreateStandalonesForReplica(s.Spec.HostnamePrefix, s.Spec.Name, s.Spec.Version, *s.Spec.Members)
	for _, member := range members {
		deployment.MergeStandalone(member)
	}
	replica := NewReplicaSet(s.Spec.Name, members)

	deployment.AddReplicaSet(&replica)

	_, err = omConnection.ApplyDeployment(deployment)
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
	deployment, err := c.context.Clientset.AppsV1().StatefulSets(newRes.Namespace).Update(BuildReplicaSet(newRes))
	if err != nil {
		fmt.Printf("Error while creating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	fmt.Printf("Updated MongoDbReplicaSet '%s' with %d replicas\n", deployment.Name, *deployment.Spec.Replicas)
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, *s.Spec.Members)
}

// CreateStandaloneForReplica returns a list of om.Standalones
func CreateStandalonesForReplica(hostnamePrefix, replicaSetName, version string, memberQty int32) []*om.Standalone {
	collection := make([]*om.Standalone, memberQty)
	qty := int(memberQty)

	for i := 0; i < qty; i++ {
		suffix := "my-service.default.svc.cluster.local"
		hostname := fmt.Sprintf("%s-%d.%s", hostnamePrefix, i, suffix)
		name := fmt.Sprintf("%s_%d", replicaSetName, i)
		member := om.NewStandalone(version).
			Name(name).
			HostPort(hostname).
			DbPath("/data").
			LogPath("/data/mongodb.log").
			ReplicaSetName(replicaSetName)
		collection[i] = member
	}

	return collection
}

func NewReplicaSet(id string, standalones []*om.Standalone) om.ReplicaSets {
	rs := om.ReplicaSets{ReplSetConfig: &core.ReplSetConfig{}}
	members := make([]core.Member, len(standalones))

	for idx, member := range standalones {
		// hostport := hosts.BuildHostPort(member.Process.Hostname, 27017)
		members[idx] = core.NewMemberWithDefaults(idx, hosts.HostPort(member.Process.Name))
	}

	rs.Id = id
	rs.Members = members
	return rs
}
