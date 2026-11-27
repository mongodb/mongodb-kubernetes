package status

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CertificateRenewalStatus reports the next automatic renewal of the operator-managed leaf
// certificates, per category. Set only with spec.security.managedCertificate.
// +kubebuilder:object:generate:=true
type CertificateRenewalStatus struct {
	// Server is the soonest upcoming renewal among the server-category (serverAuth) leaf certificates.
	// +optional
	Server *CategoryRenewal `json:"server,omitempty"`
	// Client is the soonest upcoming renewal among the client-category (clientAuth) leaf certificates.
	// +optional
	Client *CategoryRenewal `json:"client,omitempty"`
}

// CategoryRenewal identifies the leaf certificate in one category that cert-manager renews soonest.
// +kubebuilder:object:generate:=true
type CategoryRenewal struct {
	// Name of the soonest-renewing Certificate in this category.
	Name string `json:"name"`
	// NotAfter is that certificate's expiry.
	// +optional
	NotAfter *metav1.Time `json:"notAfter,omitempty"`
	// RenewalTime is when cert-manager renews it, which is when the members roll onto the new certificate.
	// +optional
	RenewalTime *metav1.Time `json:"renewalTime,omitempty"`
}
