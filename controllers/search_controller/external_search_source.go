package search_controller

import (
	"k8s.io/apimachinery/pkg/types"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
)

func NewExternalSearchSource(namespace string, spec *searchv1.ExternalMongoDBSource) SearchSourceDBResource {
	return &externalSearchResource{namespace: namespace, spec: spec}
}

// externalSearchResource implements SearchSourceDBResource for deployments managed outside the operator.
type externalSearchResource struct {
	namespace string
	spec      *searchv1.ExternalMongoDBSource
}

func (r *externalSearchResource) Validate() error {
	return nil
}

func (r *externalSearchResource) KeyfileSecretName() string {
	if r.spec.KeyFileSecretKeyRef != nil {
		return r.spec.KeyFileSecretKeyRef.Name
	}

	return ""
}

func (r *externalSearchResource) IsSecurityTLSConfigEnabled() bool {
	return r.spec.TLS != nil && r.spec.TLS.Enabled
}

func (r *externalSearchResource) TLSOperatorCASecretNamespacedName() types.NamespacedName {
	if r.spec.TLS != nil && r.spec.TLS.CA != nil {
		return types.NamespacedName{
			Name:      r.spec.TLS.CA.Name,
			Namespace: r.namespace,
		}
	}

	return types.NamespacedName{}
}

func (r *externalSearchResource) HostSeeds() []string { return r.spec.HostAndPorts }
