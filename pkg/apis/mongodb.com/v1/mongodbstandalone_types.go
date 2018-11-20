package v1

import (
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
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file

	Version string `json:"version"`
	// this is an optional service, it will get the name "<standaloneName>-service" in case not provided
	Service     string                 `json:"service,omitempty"`
	ClusterName string                 `json:"clusterName,omitempty"`
	Persistent  *bool                  `json:"persistent,omitempty"`
	PodSpec     MongoDbPodSpecStandard `json:"podSpec,omitempty"`
	Project     string                 `json:"project"`
	Credentials string                 `json:"credentials"`
}

// MongoDbStandaloneStatus defines the observed state of MongoDbStandalone
type MongoDbStandaloneStatus struct {
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file

	Version string `json:"version"`
	// TODO
	State string `json:"state"`
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

func (c *MongoDbStandalone) UpdateSuccessful() {
	c.Status.Version = c.Spec.Version

	// TODO proper implement
	c.Status.State = "Running"
}

func (c *MongoDbStandalone) UpdateError(errorMessage string) {
	// TODO proper implement
	c.Status.State = "Failed"
}
