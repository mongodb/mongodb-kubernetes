package role

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

// MongoDBCustomRoleSpec defines the desired state of MongoDBCustomRole.
type MongoDBCustomRoleSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	mdb.MongoDBRole `json:",inline"`
}

// MongoDBCustomRoleStatus defines the observed state of MongoDBCustomRole.
type MongoDBCustomRoleStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=mdbcr
// +kubebuilder:subresource:status

// MongoDBCustomRole is the Schema for the mongodbcustomroles API.
type MongoDBCustomRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MongoDBCustomRoleSpec `json:"spec,omitempty"`
	// +optional
	Status MongoDBCustomRoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MongoDBCustomRoleList contains a list of MongoDBCustomRole.
type MongoDBCustomRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBCustomRole `json:"items"`
}

func init() {
	v1.SchemeBuilder.Register(&MongoDBCustomRole{}, &MongoDBCustomRoleList{})
}
