package vai

import (
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
)

const (
	voyageRouterDeploymentSuffix = "-voyage-router"
	voyageRouterServiceSuffix    = "-voyage-router-svc"
	voyageRouterConfigMapSuffix  = "-voyage-router-config"

	VoyageRouterDefaultReplicas              int32 = 1
	VoyageRouterDefaultRequestBodyLimitBytes int64 = 33554432

	VoyageRouterConditionReady      = "Ready"
	VoyageRouterConditionReconciled = "Reconciled"
)

func init() {
	SchemeBuilder.Register(&VoyageRouter{}, &VoyageRouterList{})
}

// +k8s:deepcopy-gen=true
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the VoyageRouter deployment."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the VoyageRouter resource was created."
// +kubebuilder:resource:path=voyagerouters,scope=Namespaced,shortName=vrouter
type VoyageRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VoyageRouterSpec   `json:"spec"`
	Status VoyageRouterStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type VoyageRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []VoyageRouter `json:"items"`
}

// VoyageRouterSpec configures a model-routing Envoy proxy.
type VoyageRouterSpec struct {
	// Selector selects same-namespace VoyageAI resources. Empty selects all.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Replicas is the number of Envoy proxy pods.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// ResourceRequirements configures Envoy resource requests and limits.
	// +optional
	ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`

	// Deployment holds overrides merged into the operator-created Deployment.
	// +optional
	Deployment *mdbv1.DeploymentConfiguration `json:"deployment,omitempty"`

	// RequestBodyLimitBytes sets Envoy's maximum buffered request body size.
	// The 32 MiB default accommodates a maximum-size multimodal image or video.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=33554432
	RequestBodyLimitBytes int64 `json:"requestBodyLimitBytes,omitempty"`

	// Security configures inbound and upstream TLS.
	// +optional
	Security VoyageRouterSecurity `json:"security,omitempty"`
}

// VoyageRouterSecurity configures TLS for the Router.
type VoyageRouterSecurity struct {
	// TLS configures inbound TLS for client requests.
	// +optional
	TLS *TLS `json:"tls,omitempty"`

	// UpstreamTLS configures trust for TLS-enabled VoyageAI backends.
	// +optional
	UpstreamTLS *VoyageRouterUpstreamTLS `json:"upstreamTLS,omitempty"`
}

// VoyageRouterUpstreamTLS configures the CA used for VoyageAI backends.
type VoyageRouterUpstreamTLS struct {
	// CA references a ConfigMap containing ca.crt.
	CA corev1.LocalObjectReference `json:"ca"`
}

type VoyageRouterStatus struct {
	status.Common `json:",inline"`
	Conditions    []metav1.Condition `json:"conditions,omitempty"`
}

var (
	_ status.Reader = (*VoyageRouter)(nil)
	_ status.Writer = (*VoyageRouter)(nil)
)

func (v *VoyageRouter) GetCommonStatus(options ...status.Option) *status.Common {
	return &v.Status.Common
}

func (v *VoyageRouter) GetStatus(options ...status.Option) any {
	return v.Status
}

func (v *VoyageRouter) GetStatusPath(options ...status.Option) string {
	return "/status"
}

func (v *VoyageRouter) SetWarnings([]status.Warning, ...status.Option) {}

func (v *VoyageRouter) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	v.Status.UpdateCommonFields(phase, v.GetGeneration(), statusOptions...)
}

func (v *VoyageRouter) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Name: v.Name, Namespace: v.Namespace}
}

func (v *VoyageRouter) DeploymentName() string {
	return v.Name + voyageRouterDeploymentSuffix
}

func (v *VoyageRouter) ServiceName() string {
	return v.Name + voyageRouterServiceSuffix
}

func (v *VoyageRouter) ConfigMapName() string {
	return v.Name + voyageRouterConfigMapSuffix
}

func (v *VoyageRouter) ReplicasOrDefault() int32 {
	if v.Spec.Replicas != nil {
		return *v.Spec.Replicas
	}
	return VoyageRouterDefaultReplicas
}

func (v *VoyageRouter) RequestBodyLimitBytesOrDefault() int64 {
	if v.Spec.RequestBodyLimitBytes != 0 {
		return v.Spec.RequestBodyLimitBytes
	}
	return VoyageRouterDefaultRequestBodyLimitBytes
}

func (v *VoyageRouter) IsTLSConfigured() bool {
	return v.Spec.Security.TLS != nil
}

func (v *VoyageRouter) InboundTLSSecretNamespacedName() types.NamespacedName {
	if v.Spec.Security.TLS == nil {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: v.Spec.Security.TLS.CertificateKeySecretRef.Name, Namespace: v.Namespace}
}

func (v *VoyageRouter) UpstreamCAConfigMapNamespacedName() types.NamespacedName {
	if v.Spec.Security.UpstreamTLS == nil {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: v.Spec.Security.UpstreamTLS.CA.Name, Namespace: v.Namespace}
}
