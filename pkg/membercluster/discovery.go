// Package membercluster contains the operator-side wiring that discovers member clusters
// from MemberCluster CRs and their per-cluster credential Secrets, and watches those CRs so
// the operator can rebuild its member-cluster client map when membership changes.
package membercluster

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	restclient "k8s.io/client-go/rest"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

// credentialSecretKubeconfigKey is the Secret key holding the single-context kubeconfig.
// It matches the key written by the `generate-member-registration` plugin command.
const credentialSecretKubeconfigKey = "kubeconfig"

// Discover builds a map of member-cluster REST configs from the MemberCluster CRs in the given
// namespace. Each CR references a credential Secret whose "kubeconfig" key holds a
// single-context kubeconfig. The returned map is keyed by spec.clusterName — the logical name
// the operator uses to resolve clusterSpecList[].clusterName references in workload CRs.
//
// The bool return reports whether any MemberCluster CRs were found. false means the caller
// should fall back to the legacy discovery path (member-list ConfigMap + mounted kubeconfig).
//
// A per-cluster failure (missing or unparseable credential Secret) is logged and that cluster
// is skipped, so one broken cluster does not prevent the operator from managing the others.
// This mirrors the "don't panic, continue" behaviour of the legacy path in main.go.
func Discover(ctx context.Context, c client.Reader, namespace string, clientTimeoutSeconds int) (map[string]*restclient.Config, bool, error) {
	memberClusterList := &operatorv1.MemberClusterList{}
	if err := c.List(ctx, memberClusterList, client.InNamespace(namespace)); err != nil {
		return nil, false, fmt.Errorf("listing MemberCluster CRs in namespace %s: %w", namespace, err)
	}

	if len(memberClusterList.Items) == 0 {
		return nil, false, nil
	}

	restConfigs := map[string]*restclient.Config{}
	for i := range memberClusterList.Items {
		mc := &memberClusterList.Items[i]
		restConfig, err := restConfigFromMemberCluster(ctx, c, mc, namespace)
		if err != nil {
			zap.S().Errorf("Skipping member cluster %q (MemberCluster %q): %s", mc.Spec.ClusterName, mc.Name, err)
			continue
		}
		restConfig.Timeout = time.Duration(clientTimeoutSeconds) * time.Second
		restConfigs[mc.Spec.ClusterName] = restConfig
	}

	return restConfigs, true, nil
}

// restConfigFromMemberCluster reads the credential Secret referenced by the MemberCluster CR
// and builds a REST config from its single-context kubeconfig.
func restConfigFromMemberCluster(ctx context.Context, c client.Reader, mc *operatorv1.MemberCluster, namespace string) (*restclient.Config, error) {
	secretName := mc.Spec.CredentialSecretRef.Name
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		return nil, fmt.Errorf("reading credential secret %q: %w", secretName, err)
	}

	kubeconfig, ok := secret.Data[credentialSecretKubeconfigKey]
	if !ok || len(kubeconfig) == 0 {
		return nil, fmt.Errorf("credential secret %q has no %q key", secretName, credentialSecretKubeconfigKey)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building REST config from credential secret %q: %w", secretName, err)
	}

	return restConfig, nil
}
