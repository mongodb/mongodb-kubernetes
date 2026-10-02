package operatorconfig

import (
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	if len(cfg.Spec.WatchedResources) == 0 {
		cfg.Spec.WatchedResources = slices.Clone(operatorv1.AllWatchedResources)
	}
	// MultiCluster is a pointer, so an omitted block leaves the API server's
	// nested defaults (e.g. memberClusterClientTimeout=10) unapplied. Ensure the
	// block exists and the timeout is defaulted so member-cluster clients never
	// end up with a zero (i.e. no) timeout.
	if cfg.Spec.MultiCluster == nil {
		cfg.Spec.MultiCluster = &operatorv1.MultiClusterConfig{}
	}
	if cfg.Spec.MultiCluster.MemberClusterClientTimeout == 0 {
		cfg.Spec.MultiCluster.MemberClusterClientTimeout = 10
	}
	if cfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak == 0 {
		cfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak = 5
	}
	// AutomaticRecovery is a pointer, so an omitted block leaves the API server's nested defaults
	// (mode=Enabled, delay=1200) unapplied. Ensure the block exists and default its fields. Delay's
	// minimum is 1, so a zero value can only mean "unset" and is safe to sentinel-default.
	if cfg.Spec.AutomaticRecovery == nil {
		cfg.Spec.AutomaticRecovery = &operatorv1.AutomaticRecoveryConfig{}
	}
	if cfg.Spec.AutomaticRecovery.Mode == "" {
		cfg.Spec.AutomaticRecovery.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.AutomaticRecovery.Delay == 0 {
		cfg.Spec.AutomaticRecovery.Delay = 1200
	}
	// Proxy is a pointer, so an omitted block leaves the API server's nested default
	// (envPropagationPolicy=NoPropagation) unapplied. Ensure the block exists and default the
	// policy so proxy env vars are never propagated unless the user opts in.
	if cfg.Spec.Proxy == nil {
		cfg.Spec.Proxy = &operatorv1.ProxyConfig{}
	}
	if cfg.Spec.Proxy.EnvPropagationPolicy == "" {
		cfg.Spec.Proxy.EnvPropagationPolicy = operatorv1.ProxyEnvPropagationPolicyNoPropagation
	}
	// Telemetry is a pointer tree, so an omitted block leaves the API server's nested defaults
	// unapplied. Absence of any telemetry configuration must imply telemetry is enabled (opt-out
	// model), so ensure every nested block exists and its fields are defaulted to Enabled with the
	// documented frequencies/timeout.
	if cfg.Spec.Telemetry == nil {
		cfg.Spec.Telemetry = &operatorv1.TelemetryConfig{}
	}
	if cfg.Spec.Telemetry.Mode == "" {
		cfg.Spec.Telemetry.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.Telemetry.Collection == nil {
		cfg.Spec.Telemetry.Collection = &operatorv1.TelemetryCollectionConfig{}
	}
	if cfg.Spec.Telemetry.Collection.Frequency == nil {
		cfg.Spec.Telemetry.Collection.Frequency = &metav1.Duration{Duration: time.Hour}
	}
	if cfg.Spec.Telemetry.Collection.KubeTimeout == nil {
		cfg.Spec.Telemetry.Collection.KubeTimeout = &metav1.Duration{Duration: 5 * time.Minute}
	}
	if cfg.Spec.Telemetry.Collection.Clusters == nil {
		cfg.Spec.Telemetry.Collection.Clusters = &operatorv1.TelemetryCollectionClustersConfig{}
	}
	if cfg.Spec.Telemetry.Collection.Clusters.Mode == "" {
		cfg.Spec.Telemetry.Collection.Clusters.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.Telemetry.Collection.Deployments == nil {
		cfg.Spec.Telemetry.Collection.Deployments = &operatorv1.TelemetryCollectionDeploymentsConfig{}
	}
	if cfg.Spec.Telemetry.Collection.Deployments.Mode == "" {
		cfg.Spec.Telemetry.Collection.Deployments.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.Telemetry.Collection.Operators == nil {
		cfg.Spec.Telemetry.Collection.Operators = &operatorv1.TelemetryCollectionOperatorsConfig{}
	}
	if cfg.Spec.Telemetry.Collection.Operators.Mode == "" {
		cfg.Spec.Telemetry.Collection.Operators.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.Telemetry.Send == nil {
		cfg.Spec.Telemetry.Send = &operatorv1.TelemetrySendConfig{}
	}
	if cfg.Spec.Telemetry.Send.Mode == "" {
		cfg.Spec.Telemetry.Send.Mode = operatorv1.FeatureModeEnabled
	}
	if cfg.Spec.Telemetry.Send.Frequency == nil {
		cfg.Spec.Telemetry.Send.Frequency = &metav1.Duration{Duration: 168 * time.Hour}
	}
	return cfg
}
