package operatorconfig

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

// Load fetches the OperatorConfig CR with the given name from the given namespace.
// If no CR exists, a fully-defaulted config is returned with an empty ResourceVersion.
func Load(ctx context.Context, c client.Reader, namespace, name string) (operatorv1.OperatorConfig, error) {
	var cfg operatorv1.OperatorConfig
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &cfg)
	if apierrors.IsNotFound(err) {
		return withDefaults(operatorv1.OperatorConfig{}), nil
	}
	if err != nil {
		return operatorv1.OperatorConfig{}, fmt.Errorf("getting OperatorConfig %q in namespace %q: %w", name, namespace, err)
	}
	return withDefaults(cfg), nil
}

// withDefaults fills in Go-side defaults that mirror the CRD schema markers.
// Required when constructing a spec without the API server's defaulting webhook.
func withDefaults(cfg operatorv1.OperatorConfig) operatorv1.OperatorConfig {
	if cfg.Spec.DefaultArchitecture == "" {
		cfg.Spec.DefaultArchitecture = operatorv1.ArchitectureNonStatic
	}
	if cfg.Spec.MaxConcurrentReconciles == 0 {
		cfg.Spec.MaxConcurrentReconciles = 1
	}
	return cfg
}
