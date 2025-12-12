package certificate

import (
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

type ResourceType string

type MongoDBCertificateSpec struct {
	// +kubebuilder:validation:Enum=tls
	// +kubebuilder:validation:Required
	ResourceType ResourceType `json:"type"`
	//+optional
	IssuerRef *IssuerRef `json:"issuerRef,omitempty"`
	// +kubebuilder:validation:Required
	MongoDBResourceRef userv1.MongoDBResourceRef `json:"mongodbResourceRef"`
	// +kubebuilder:validation:Required
	CertificateWrapper CertificateSpecWrapper `json:"certificateSpec,inline"`
}

type IssuerRef struct {
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
