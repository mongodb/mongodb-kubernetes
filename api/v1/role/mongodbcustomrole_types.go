package role

import (
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
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
	status.Common `json:",inline"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=mdbcr
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The current state of the MongoDB Custom Role."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDB Custom Role resource was created."

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

func (r *MongoDBCustomRole) GetStatus(...status.Option) interface{} {
	return r.Status
}

func (r *MongoDBCustomRole) GetCommonStatus(options ...status.Option) *status.Common {
	return &r.Status.Common
}

func (r *MongoDBCustomRole) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	r.Status.UpdateCommonFields(phase, r.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		r.Status.Warnings = append(r.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}

	if phase == status.PhaseRunning {
		r.Status.Phase = status.PhaseUpdated
	}
}

func (r *MongoDBCustomRole) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	r.Status.Warnings = warnings
}

func (r *MongoDBCustomRole) GetStatusPath(options ...status.Option) string {
	return "/status"
}
