package searchcontroller

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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
// central is checked.
func CheckSecretsPresence(
	ctx context.Context,
	search *searchv1.MongoDBSearch,
	central client.Client,
	members map[string]client.Client,
) []SecretCheckResult {
	expected := expectedSecretNames(search)
	if len(expected) == 0 {
		return nil
	}

	results := make([]SecretCheckResult, 0, len(members)+1)

	if missing := missingSecretsIn(ctx, central, search.Namespace, expected); len(missing) > 0 {
		results = append(results, SecretCheckResult{Cluster: "", Missing: missing})
	}

	for clusterName, c := range members {
		if missing := missingSecretsIn(ctx, c, search.Namespace, expected); len(missing) > 0 {
			results = append(results, SecretCheckResult{Cluster: clusterName, Missing: missing})
		}
	}

	return results
}

// expectedSecretNames returns the deduplicated list of secret names the customer is
// expected to replicate into every member cluster's namespace, derived from the CR.
// Order is stable so logs and test assertions are deterministic.
func expectedSecretNames(search *searchv1.MongoDBSearch) []string {
	var names []string

	// Sync-source password — always required when a password ref is configured.
	if ref := search.SourceUserPasswordSecretRef(); ref != nil && ref.Name != "" {
		names = appendUnique(names, ref.Name)
	}

	// External CA bundle — Q2-MC only.
	if search.IsExternalMongoDBSource() {
		ext := search.Spec.Source.ExternalMongoDBSource
		if ext.TLS != nil && ext.TLS.CA != nil && ext.TLS.CA.Name != "" {
			names = appendUnique(names, ext.TLS.CA.Name)
		}
		// Keyfile — sharded only.
		if search.IsExternalSourceSharded() && ext.KeyFileSecretKeyRef != nil && ext.KeyFileSecretKeyRef.Name != "" {
			names = appendUnique(names, ext.KeyFileSecretKeyRef.Name)
		}
	}

	// mongot server TLS cert per unit (single RS or per shard) + Envoy server TLS cert.
	// Both share the same `<prefix>-...-cert` family so listing the mongot cert covers
	// the Envoy expectation in Q2-MC where Envoy reuses the per-shard cert.
	if search.Spec.Security.TLS != nil {
		if search.IsExternalSourceSharded() {
			for _, shard := range search.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
				names = appendUnique(names, search.TLSSecretForShard(shard.ShardName).Name)
			}
		} else {
			names = appendUnique(names, search.TLSSecretNamespacedName().Name)
		}
	}

	// x509 client cert — only when x509 auth is configured.
	if search.IsX509Auth() {
		names = appendUnique(names, search.X509ClientCertSecret().Name)
	}

	return names
}

// missingSecretsIn does a Get on each name and returns names that are NotFound.
// Other errors (RBAC, transport) are conservatively treated as "missing" because
// from the reconciler's POV the secret is not usable. This is a deliberate simplification
// for the MVP — B9 will distinguish between NotFound and operational errors when it adds
// the per-cluster status surface.
func missingSecretsIn(ctx context.Context, c client.Client, namespace string, names []string) []string {
	if c == nil {
		return nil
	}
	var missing []string
	for _, name := range names {
		var s corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, name)
			} else {
				// Treat operational errors as missing for MVP. B9 will refine.
				missing = append(missing, name)
			}
		}
	}
	return missing
}

func appendUnique(in []string, name string) []string {
	for _, existing := range in {
		if existing == name {
			return in
		}
	}
	return append(in, name)
}
