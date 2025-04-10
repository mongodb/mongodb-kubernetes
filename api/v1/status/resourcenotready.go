package status

// ResourceKind specifies a kind of a Kubernetes resource. Used in status of a Custom Resource
type ResourceKind string

const (
	StatefulsetKind ResourceKind = "StatefulSet"
)

// ResourceNotReady describes the dependent resource which is not ready yet
// +k8s:deepcopy-gen=true
type ResourceNotReady struct {
	Kind    ResourceKind    `json:"kind"`
	Name    string          `json:"name"`
	Errors  []ResourceError `json:"errors,omitempty"`
	Message string          `json:"message,omitempty"`
}

// +k8s:deepcopy-gen=true
type ResourceError struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}
