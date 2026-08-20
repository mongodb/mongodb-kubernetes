package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MongoDBDirectiveSpec defines the desired state of MongoDBDirective.
// The spec is written only by the leader operator — the single writer — which may run on
// another cluster. The local member operator never writes the spec.
type MongoDBDirectiveSpec struct {
	// ClusterName is the logical cluster identity of the member this directive addresses,
	// matching clusterSpecList[].clusterName references in the workload CR.
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`

	// LeadershipTerm is the term of the leader that wrote this spec. Members refuse directives
	// whose term is older than the term of their local Lease (the term fence).
	LeadershipTerm int64 `json:"leadershipTerm"`

	// TargetSpecHash is the hash of the workload CR spec this directive was planned from.
	// The member acts only when the hash of its local CR copy matches (the spec fence);
	// otherwise it holds and keeps reporting status.
	// +kubebuilder:validation:MinLength=1
	TargetSpecHash string `json:"targetSpecHash"`

	// MemberCount is the member count the local StatefulSet is granted to advance to.
	MemberCount int `json:"memberCount"`

	// ClusterIndex is the index allocated to this cluster, used for object naming.
	ClusterIndex int `json:"clusterIndex"`

	// IndexAllocations is the full historical clusterName to index map. It is grow-only:
	// entries are never removed, so a re-added cluster gets its old index back.
	// +optional
	IndexAllocations map[string]int `json:"indexAllocations,omitempty"`
}

// MongoDBDirectiveStatus defines the observed state of MongoDBDirective.
// The status is written only by the member operator local to the addressed cluster. It reports
// facts; classifying those facts is internal to the leader's planner and never serialized.
type MongoDBDirectiveStatus struct {
	// ObservedGeneration is the directive's metadata.generation last seen by the member.
	// It means "I have seen instruction #N", not "I obeyed it".
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedSpecHash is the hash of the member's local workload CR copy.
	// +optional
	ObservedSpecHash string `json:"observedSpecHash,omitempty"`

	// StsApplied reports that the StatefulSet at the granted member count was accepted by the
	// local API server.
	// +optional
	StsApplied bool `json:"stsApplied,omitempty"`

	// AgentRegistered reports that the automation agents of the granted members registered
	// with Ops Manager.
	// +optional
	AgentRegistered bool `json:"agentRegistered,omitempty"`

	// InGoalState reports that all local processes reached goal state for the current
	// automation config version.
	// +optional
	InGoalState bool `json:"inGoalState,omitempty"`
}

// MongoDBDirective carries the leader operator's instructions to one member cluster in a
// decentralized multi-cluster deployment. One directive per member cluster resides on that
// cluster's API server: the leader writes the spec (possibly from another cluster), the local
// member operator writes the status. The leadershipTerm and targetSpecHash fields fence out
// stale leaders and stale spec copies.
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mongodbdirectives,scope=Namespaced,shortName=mdbdir
// +kubebuilder:printcolumn:name="Cluster Name",type="string",JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Term",type="integer",JSONPath=".spec.leadershipTerm"
// +kubebuilder:printcolumn:name="Member Count",type="integer",JSONPath=".spec.memberCount"
// +kubebuilder:printcolumn:name="In Goal State",type="boolean",JSONPath=".status.inGoalState"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type MongoDBDirective struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBDirectiveSpec   `json:"spec,omitempty"`
	Status MongoDBDirectiveStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MongoDBDirectiveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBDirective `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MongoDBDirective{}, &MongoDBDirectiveList{})
}
