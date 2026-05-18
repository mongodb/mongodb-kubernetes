package searchcontroller

import (
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
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

// CheckSecretsPresence iterates the central cluster (always) plus every member cluster
// and verifies that the customer-replicated secrets derived from search.Spec are
// present. It only does Get; it never mutates any secret in any cluster.
//
// Returns one SecretCheckResult per cluster that has at least one missing secret;
// clusters with no gaps are omitted. The returned slice is empty (or nil) if every
// expected secret is present everywhere.
//
// members may be nil or empty for single-cluster installs; in that case only
// central is checked. clusterMapping maps member cluster name → persisted index
// used to address that cluster's per-shard TLS secrets; central uses index 0.
func CheckSecretsPresence(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	central client.Client,
	members map[string]client.Client,
	clusterMapping map[string]int,
) []SecretCheckResult {
	results := make([]SecretCheckResult, 0, len(members)+1)
	check := func(clusterName string, c client.Client, clusterIndex int) {
		expected := expectedSecretNamesForCluster(search, clusterIndex)
		if len(expected) == 0 {
			return
		}
		if missing := missingSecretsIn(ctx, c, search.Namespace, expected); len(missing) > 0 {
			results = append(results, SecretCheckResult{Cluster: clusterName, Missing: missing})
		}
	}
	check("", central, 0)
	for name, c := range members {
		check(name, c, clusterMapping[name])
	}
	return results
}

// expectedSecretNamesForCluster returns the deduplicated, sorted list of secret
// names the customer is expected to have in the given cluster's namespace.
// Per-shard TLS cert names are scoped to clusterIndex; all other names are
// cluster-invariant.
func expectedSecretNamesForCluster(search *searchv1.MongoDBSearch, clusterIndex int) []string {
	var names []string

	// Sync-source password — always required when a password ref is configured.
	if ref := search.SourceUserPasswordSecretRef(); ref != nil && ref.Name != "" {
		names = append(names, ref.Name)
	}

	// External CA bundle — only required for external MongoDB sources.
	if search.IsExternalMongoDBSource() {
		ext := search.Spec.Source.ExternalMongoDBSource
		if ext.TLS != nil && ext.TLS.CA != nil && ext.TLS.CA.Name != "" {
			names = append(names, ext.TLS.CA.Name)
		}
		// Keyfile — sharded only.
		if search.IsExternalSourceSharded() && ext.KeyFileSecretKeyRef != nil && ext.KeyFileSecretKeyRef.Name != "" {
			names = append(names, ext.KeyFileSecretKeyRef.Name)
		}
	}

	// mongot server TLS cert per unit (single RS or per shard) + Envoy server TLS cert.
	if search.Spec.Security.TLS != nil {
		if search.IsExternalSourceSharded() {
			for _, shard := range search.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
				names = append(names, search.TLSSecretForClusterShard(clusterIndex, shard.ShardName).Name)
			}
		} else {
			names = append(names, search.TLSSecretNamespacedName().Name)
		}
	}

	// x509 client cert — only when x509 auth is configured.
	if search.IsX509Auth() {
		names = append(names, search.X509ClientCertSecret().Name)
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
