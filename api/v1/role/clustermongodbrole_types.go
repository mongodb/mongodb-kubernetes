package role

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

// ClusterMongoDBRoleSpec defines the desired state of ClusterMongoDBRole.
type ClusterMongoDBRoleSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	mdbv1.MongoDBRole `json:",inline"`
}

// ClusterMongoDBRoleStatus defines the observed state of ClusterMongoDBRole.
type ClusterMongoDBRoleStatus struct {
	status.Common `json:",inline"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Cluster,shortName=cmbdr
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The current state of the MongoDB Custom Role."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB Custom Role resource was created."

// ClusterMongoDBRole is the Schema for the clustermongodbroles API.
type ClusterMongoDBRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterMongoDBRoleSpec `json:"spec,omitempty"`
	// +optional
	Status ClusterMongoDBRoleStatus `json:"status,omitempty"`
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

func (r *ClusterMongoDBRole) GetStatus(...status.Option) interface{} {
	return r.Status
}

func (r *ClusterMongoDBRole) GetCommonStatus(...status.Option) *status.Common {
	return &r.Status.Common
}

func (r *ClusterMongoDBRole) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	r.Status.UpdateCommonFields(phase, r.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		r.Status.Warnings = append(r.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		r.Status.Phase = status.PhaseUpdated
	}
}

func (r *ClusterMongoDBRole) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	r.Status.Warnings = warnings
}

func (r *ClusterMongoDBRole) GetStatusPath(...status.Option) string {
	return "/status"
}
