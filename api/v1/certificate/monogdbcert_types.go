package certificate

import (
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBCertificate{}, &MongoDBCertificateList{})
}

// The MongoDBCertificate resource

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=mdbcert
// +kubebuilder:subresource:status
type MongoDBCertificate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBCertificateStatus `json:"status"`
	Spec   MongoDBCertificateSpec   `json:"spec"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBCertificateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBCertificate `json:"items"`
}

type ResourceType string

// TODO: maybe we can have single MongoDBCertificateSpec for all resources?
// TODO: specific inner spec for each certificate type?
type MongoDBCertificateSpec struct {
	// +kubebuilder:validation:Enum=tls
	// +kubebuilder:validation:Required
	ResourceType ResourceType `json:"type"`
	//+optional
	IssuerRef *IssuerRef `json:"issuerRef,omitempty"`
	// +kubebuilder:validation:Required
	ResourceRef ResourceRef `json:"resourceRef"`
	// +kubebuilder:validation:Required
	// +kubebuilder:pruning:PreserveUnknownFields
	CertificateWrapper CertificateSpecWrapper `json:"certificateSpec"`
}

type IssuerRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type ResourceRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type CertificateSpecWrapper struct {
	CertificateSpec *cmv1.CertificateSpec `json:"-"`
}

type MongoDBCertificateStatus struct {
	status.Common `json:",inline"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

func (m *MongoDBCertificate) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m *MongoDBCertificate) GetStatusWarnings() []status.Warning {
	return m.Status.Warnings
}

func (m *MongoDBCertificate) GetCommonStatus(...status.Option) *status.Common {
	return &m.Status.Common
}

func (m *MongoDBCertificate) GetPhase() status.Phase {
	return m.Status.Phase
}

func (m *MongoDBCertificate) GetStatusPath(...status.Option) string {
	return "/status"
}

func (m *MongoDBCertificate) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	m.Status.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)
}

func (m *MongoDBCertificate) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	m.Status.Warnings = warnings
}
