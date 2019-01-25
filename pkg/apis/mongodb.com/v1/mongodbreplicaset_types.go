package v1

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
	CommonStatus
	Members int `json:"members"`
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

func (c *MongoDbReplicaSet) UpdateSuccessful(deploymentLink string, reconciledResource MongoDbResource) {
	spec := reconciledResource.(*MongoDbReplicaSet).Spec
	specHash, err := util.Hash(spec)
	if err != nil { // invalid specHash will cause infinite Reconcile loop
		panic(err)
	}
	c.Status.Version = spec.Version
	c.Status.Members = spec.Members
	c.Status.Message = ""
	c.Status.Link = deploymentLink
	c.Status.LastTransition = util.Now()
	c.Status.SpecHash = specHash
	c.Status.OperatorVersion = util.OperatorVersion
	c.Status.Phase = PhaseRunning
}

func (c *MongoDbReplicaSet) UpdateError(msg string) {
	c.Status.Message = msg
	c.Status.LastTransition = util.Now()
	c.Status.Phase = PhaseFailed
}

func (c *MongoDbReplicaSet) IsEmpty() bool {
	return c.Spec.Members == 0 &&
		c.Spec.Version == "" &&
		c.Spec.Project == "" &&
		c.Spec.Credentials == ""
}

func (c *MongoDbReplicaSet) ComputeSpecHash() (uint64, error) {
	return util.Hash(c.Spec)
}

func (c *MongoDbReplicaSet) GetCommonStatus() *CommonStatus {
	return &c.Status.CommonStatus
}

func (c *MongoDbReplicaSet) GetMeta() *Meta {
	return &c.Meta
}
