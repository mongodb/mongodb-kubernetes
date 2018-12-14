package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&MongoDbReplicaSet{}, &MongoDbReplicaSetList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
type MongoDbReplicaSet struct {
	Meta
	Spec   MongoDbReplicaSetSpec   `json:"spec"`
	Status MongoDbReplicaSetStatus `json:"status,omitempty"`
}

// MongoDbReplicaSetSpec defines the desired state of MongoDbReplicaSet
type MongoDbReplicaSetSpec struct {
	CommonSpec
	Members int            `json:"members"`
	PodSpec MongoDbPodSpec `json:"podSpec,omitempty"`
}

// MongoDbReplicaSetStatus defines the observed state of MongoDbReplicaSet
type MongoDbReplicaSetStatus struct {
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file

	Version string `json:"version"`

	// TODO
	State   string `json:"state"`
	Members int    `json:"members"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDbReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbReplicaSet `json:"items"`
}

func (c *MongoDbReplicaSet) ServiceName() string {
	return getServiceOrDefault(c.Spec.Service, c.Name, "-svc")
}

func (c *MongoDbReplicaSet) UpdateSuccessful() {
	c.Status.Version = c.Spec.Version
	c.Status.Members = c.Spec.Members

	// TODO proper implement
	c.Status.State = StateRunning
}

func (c *MongoDbReplicaSet) UpdateError(_ string) {
	// TODO proper implement
	c.Status.State = StateFailed
}

func (c *MongoDbReplicaSet) GetStatus() string {
	// TODO proper implement
	return c.Status.State
}

func (c *MongoDbReplicaSet) IsEmpty() bool {
	return c.Spec.Members == 0 &&
		c.Spec.Version == "" &&
		c.Spec.Project == "" &&
		c.Spec.Credentials == ""
}
