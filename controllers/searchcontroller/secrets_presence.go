package searchcontroller

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
)

// SecretCheckResult records the customer-replicated secrets that are missing in a
// single member cluster. Empty Missing is filtered out by CheckSecretsPresence;
// callers receive only entries that have at least one gap.
//
// Cluster is the cluster name as it appears in the operator's member-cluster map,
// or the empty string for the central / single-cluster case.
type SecretCheckResult struct {
	Cluster string
	Missing []string
}

// CheckSecretsPresence Gets each expected secret in central + every member cluster and reports gaps.
// In MC mode central is checked for cluster-invariant secrets only; per-shard certs live where mongot
// runs (member clusters at their persisted indices).
func CheckSecretsPresence(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	central client.Client,
	members map[string]client.Client,
) []SecretCheckResult {
	results := make([]SecretCheckResult, 0, len(members)+1)
	appendIfMissing := func(clusterName string, c client.Client, names []string) {
		if len(names) == 0 {
			return
		}
		if missing := missingSecretsIn(ctx, c, search.Namespace, names); len(missing) > 0 {
			results = append(results, SecretCheckResult{Cluster: clusterName, Missing: missing})
		}
	}

	if len(members) == 0 {
		// Single-cluster or operator-per-cluster with unified CR. In operator-per-cluster with unified CR the narrowed 1-entry
		// spec.clusters may be pinned to a non-zero index; index-keyed names
		// (per-shard TLS certs) must be probed at that index, not 0.
		appendIfMissing("", central, expectedSecretNamesForCluster(search, ResolveSingleClusterIndex(search)))
		return results
	}

	indexByCluster := make(map[string]int, len(search.Spec.Clusters))
	for _, c := range search.Spec.Clusters {
		indexByCluster[c.Name] = c.ResolveIndex()
	}

	appendIfMissing("", central, expectedClusterInvariantSecretNames(search))
	for name, c := range members {
		appendIfMissing(name, c, expectedSecretNamesForCluster(search, indexByCluster[name]))
	}
	return results
}

func expectedSecretNamesForCluster(search *searchv1.MongoDBSearch, clusterIndex int) []string {
	names := expectedClusterInvariantSecretNames(search)
	// MVP: multi-cluster sharded is external-source only. An operator-managed
	// multi-cluster sharded source will also need to fan out per-shard cert
	// expectations from the operator-managed shard list.
	if search.Spec.Security.TLS != nil && search.IsExternalSourceSharded() {
		for _, shard := range search.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
			names = append(names, search.TLSSecretForClusterShard(clusterIndex, shard.ShardName).Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func expectedClusterInvariantSecretNames(search *searchv1.MongoDBSearch) []string {
	var names []string
	if ref := search.SourceUserPasswordSecretRef(); ref != nil && ref.Name != "" {
		names = append(names, ref.Name)
	}
	if search.IsExternalMongoDBSource() {
		ext := search.Spec.Source.ExternalMongoDBSource
		if ext.TLS != nil && ext.TLS.CA != nil && ext.TLS.CA.Name != "" {
			names = append(names, ext.TLS.CA.Name)
		}
		if search.IsExternalSourceSharded() && ext.KeyFileSecretKeyRef != nil && ext.KeyFileSecretKeyRef.Name != "" {
			names = append(names, ext.KeyFileSecretKeyRef.Name)
		}
	}
	// RS source TLS cert name is cluster-invariant; per-shard sharded certs handled by the caller.
	if search.Spec.Security.TLS != nil && !search.IsExternalSourceSharded() {
		names = append(names, search.TLSSecretNamespacedName().Name)
	}
	if search.IsX509Auth() {
		names = append(names, search.X509ClientCertSecret().Name)
	}
	if search.HasScramClientCert() {
		names = append(names, search.ScramClientCertSecret().Name)
	}
	// Dedicated keyFilePassword secrets (customer-replicated per cluster, cluster-invariant).
	for _, nn := range []types.NamespacedName{
		search.GrpcKeyFilePasswordSecret(),
		search.X509KeyFilePasswordSecret(),
		search.ScramKeyFilePasswordSecret(),
	} {
		if nn.Name != "" {
			names = append(names, nn.Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// missingSecretsIn does a Get on each name and returns names that are not accessible.
// RBAC and transport errors are treated the same as NotFound: the secret is not
// usable from the reconciler's perspective regardless of the root cause.
func missingSecretsIn(ctx context.Context, c client.Client, namespace string, names []string) []string {
	if c == nil {
		return nil
	}
	var missing []string
	for _, name := range names {
		var s corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
			missing = append(missing, name)
		}
	}
	return missing
}
