package role

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

// ClusterMongoDBRoleSpec defines the desired state of ClusterMongoDBRole.
type ClusterMongoDBRoleSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	mdbv1.MongoDBRole `json:",inline"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Cluster,shortName=cmdbr
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB Custom Role resource was created."

// ClusterMongoDBRole is the Schema for the clustermongodbroles API.
type ClusterMongoDBRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterMongoDBRoleSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterMongoDBRoleList contains a list of ClusterMongoDBRole.
type ClusterMongoDBRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterMongoDBRole `json:"items"`
}

func init() {
	v1.SchemeBuilder.Register(&ClusterMongoDBRole{}, &ClusterMongoDBRoleList{})
}
