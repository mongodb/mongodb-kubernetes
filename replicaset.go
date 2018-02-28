package main

import (
	"fmt"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"com.tengen/cm/core"
	"com.tengen/cm/hosts"
	om "github.com/10gen/ops-manager-kubernetes/om"
)

// BuildReplicaSet will return a StatefulSet definition, built on top of Pods.
func BuildReplicaSet(obj *mongodb.MongoDbReplicaSet) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":   LabelApp,
		"hosts": obj.Spec.HostnamePrefix,
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
			Replicas: obj.Spec.Members,
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

	name_prefix := "replica"
	hostname0 := fmt.Sprintf("%s-0", s.Spec.HostnamePrefix)
	hostname1 := fmt.Sprintf("%s-1", s.Spec.HostnamePrefix)
	hostname2 := fmt.Sprintf("%s-2", s.Spec.HostnamePrefix)
	name0 := fmt.Sprintf("%s_0", name_prefix)
	name1 := fmt.Sprintf("%s_1", name_prefix)
	name2 := fmt.Sprintf("%s_2", name_prefix)
	member0 := om.NewStandalone(s.Spec.Version).
		Name(name0).
		HostPort(hostname0).
		DbPath("/data").
		LogPath("/data/mongodb.log").
		ReplicaSetName("rs01")
	member1 := om.NewStandalone(s.Spec.Version).
		Name(name1).
		HostPort(hostname1).
		DbPath("/data").
		LogPath("/data/mongodb.log").
		ReplicaSetName("rs01")
	member2 := om.NewStandalone(s.Spec.Version).
		Name(name2).
		HostPort(hostname2).
		DbPath("/data").
		LogPath("/data/mongodb.log").
		ReplicaSetName("rs01")

	deployment := om.NewDeployment("3.6.3")
	deployment.AddStandaloneProcess(member0.Process)
	deployment.AddStandaloneProcess(member1.Process)
	deployment.AddStandaloneProcess(member2.Process)

	members := []*om.Standalone{member0, member1, member2}
	replica := NewReplicaSet("rs01", members)

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
