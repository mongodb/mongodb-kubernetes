package searchcontroller

import (
	"k8s.io/apimachinery/pkg/types"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

func NewExternalSearchSource(namespace string, spec *searchv1.ExternalMongoDBSource) SearchSourceDBResource {
	return &externalSearchResource{namespace: namespace, spec: spec}
}

// externalSearchResource implements SearchSourceDBResource for deployments managed outside the Kubernetes cluster.
type externalSearchResource struct {
	namespace string
	spec      *searchv1.ExternalMongoDBSource
}

func (r *externalSearchResource) Validate() error {
	// We don't know anything about the external MongoDB deployment, so we can't validate it.
	// Perhaps in the future the Operator could attempt to connect to the external MongoDB instance
	// and validate its configuration.
	return nil
}

func (r *externalSearchResource) TLSConfig() *TLSSourceConfig {
	if r.spec.TLS == nil {
		return nil
	}

	return &TLSSourceConfig{
		CAFileName: "ca.crt",
		CAVolume:   statefulset.CreateVolumeFromSecret("ca", r.spec.TLS.CA.Name),
		ResourcesToWatch: map[watch.Type][]types.NamespacedName{
			watch.Secret: {
				{Namespace: r.namespace, Name: r.spec.TLS.CA.Name},
			},
		},
	}
}

func (r *externalSearchResource) KeyfileSecretName() string {
	if r.spec.KeyFileSecretKeyRef != nil {
		return r.spec.KeyFileSecretKeyRef.Name
	}

	return ""
}

func (r *externalSearchResource) HostSeeds() []string { return r.spec.HostAndPorts }
