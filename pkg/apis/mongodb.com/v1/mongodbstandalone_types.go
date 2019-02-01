package v1

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

func init() {
	SchemeBuilder.Register(&MongoDbStandalone{}, &MongoDbStandaloneList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MongoDbStandalone is the Schema for the mongodbstandalones API
// +k8s:openapi-gen=true
type MongoDbStandalone struct {
	Meta
	Spec   MongoDbStandaloneSpec   `json:"spec,omitempty"`
	Status MongoDbStandaloneStatus `json:"status,omitempty"`
}

// MongoDbStandaloneSpec defines the desired state of MongoDbStandalone
type MongoDbStandaloneSpec struct {
	CommonSpec
	PodSpec MongoDbPodSpecStandard `json:"podSpec,omitempty"`
}

// MongoDbStandaloneStatus defines the observed state of MongoDbStandalone
type MongoDbStandaloneStatus struct {
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file
	CommonStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MongoDbStandaloneList contains a list of MongoDbStandalone
type MongoDbStandaloneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDbStandalone `json:"items"`
}

func (c *MongoDbStandalone) ServiceName() string {
	return getServiceOrDefault(c.Spec.Service, c.Name, "-svc")
}

func (c *MongoDbStandalone) UpdateSuccessful(deploymentLink string, reconciledResource MongoDbResource) {
	spec := reconciledResource.(*MongoDbStandalone).Spec
	c.Status.Version = spec.Version
	c.Status.Message = ""
	c.Status.Link = deploymentLink
	c.Status.LastTransition = util.Now()
	c.Status.Phase = PhaseRunning
}

func (c *MongoDbStandalone) UpdateError(errorMessage string) {
	c.Status.Message = errorMessage
	c.Status.LastTransition = util.Now()
	c.Status.Phase = PhaseFailed
}

func (c *MongoDbStandalone) GetCommonStatus() *CommonStatus {
	return &c.Status.CommonStatus
}

func (c *MongoDbStandalone) GetMeta() *Meta {
	return &c.Meta
}
