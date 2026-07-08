package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MemberClusterSpec defines the desired state of MemberCluster.
type MemberClusterSpec struct {
	// ClusterName is the logical cluster identity used to resolve clusterSpecList[].clusterName
	// references in workload CRs (e.g. MongoDBMultiCluster). In most cases this matches
	// metadata.name; they differ only when the logical name is not RFC 1123 compliant,
	// for example during MCK 1.x to 2.x migration where existing workload CRs reference
	// names that must not be modified.
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`

	// CredentialSecretRef references the Secret that contains a single-context kubeconfig
	// the operator uses to authenticate against this member cluster.
	// The Secret must be in the same namespace as the MemberCluster CR.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.name != ''",message="credentialSecretRef.name must not be empty"
	CredentialSecretRef corev1.LocalObjectReference `json:"credentialSecretRef"`
}

// MemberClusterStatus defines the observed state of MemberCluster.
type MemberClusterStatus struct {
	// Conditions reflect the current status of the member cluster.
	// The RBACValid condition reports whether the member-cluster RBAC version matches
	// the running operator version. When RBACValid is False the operator stops reconciling
	// workloads on the cluster until RBAC is regenerated and reapplied.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MemberCluster represents a member cluster in a multi-cluster MCK deployment.
// The operator discovers member clusters by watching MemberCluster CRs in its namespace.
// Each CR references a per-cluster credential Secret containing a single-context kubeconfig.
// Adding a cluster means creating a MemberCluster CR and its credential Secret; removing
// a cluster means deleting the MemberCluster CR. No operator restart is required.
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=memberclusters,scope=Namespaced,shortName=mc
// +kubebuilder:printcolumn:name="Cluster Name",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="RBAC Valid",type="string",JSONPath=".status.conditions[?(@.type==\"RBACValid\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type MemberCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MemberClusterSpec   `json:"spec,omitempty"`
	Status MemberClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MemberClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MemberCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MemberCluster{}, &MemberClusterList{})
}
