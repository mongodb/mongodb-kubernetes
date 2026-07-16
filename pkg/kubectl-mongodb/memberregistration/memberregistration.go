// Package memberregistration produces the registration a member cluster needs so the MCK
// operator can reach it: a credential Secret (a single-context kubeconfig) and a MemberCluster
// CR referencing that Secret. It reads the ServiceAccount token that
// `generate-member-resources` created on the member cluster and writes both resources as a
// multi-document YAML string. It holds the logic; the CLI wiring lives in cmd/kubectl-mongodb.
package memberregistration

import (
	"context"

	"golang.org/x/xerrors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

const (
	// credentialSecretKey is the single key in the credential Secret holding the kubeconfig.
	credentialSecretKey = "kubeconfig"

	// tokenSecretTokenKey and tokenSecretCAKey are the keys Kubernetes populates on a
	// kubernetes.io/service-account-token Secret.
	tokenSecretTokenKey = "token"
	tokenSecretCAKey    = "ca.crt"
)

// Options carries the resolved flag values for a single member-cluster registration.
type Options struct {
	// MemberClusterName is the RFC 1123 name used for the MemberCluster CR's metadata.name and the
	// credential Secret name suffix. It must match the name passed to generate-member-resources,
	// which is how the token Secret (mck-member-<MemberClusterName>-token) is located.
	MemberClusterName string
	// MemberClusterLogicalName is the logical cluster identity set as spec.clusterName on the MemberCluster CR.
	// Used to resolve clusterSpecList[].clusterName references in workload CRs.
	MemberClusterLogicalName string
	// MemberClusterNamespace is the namespace on the member cluster holding the SA token Secret.
	MemberClusterNamespace string
	// OperatorNamespace is the namespace on the operator's cluster where the emitted CR and
	// credential Secret are placed.
	OperatorNamespace string
}

// Generate reads the member ServiceAccount token Secret via client and builds the output using
// serverURL as the kubeconfig API-server address. It is the entry point used by the CLI, which
// builds client and serverURL from the member cluster's kubeconfig context.
func Generate(ctx context.Context, memberClusterClient kubernetes.Interface, memberClusterServerURL string, opts Options) (string, error) {
	tokenSecretName := resourcenames.MemberClusterTokenSecretName(opts.MemberClusterName)
	tokenSecret, err := memberClusterClient.CoreV1().Secrets(opts.MemberClusterNamespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return "", xerrors.Errorf("reading token secret %s/%s on the member cluster (was 'generate-member-resources' applied to it?): %w", opts.MemberClusterNamespace, tokenSecretName, err)
	}

	token, ok := tokenSecret.Data[tokenSecretTokenKey]
	if !ok || len(token) == 0 {
		return "", xerrors.Errorf("token secret %s/%s has no %q key yet; wait for Kubernetes to populate the ServiceAccount token", opts.MemberClusterNamespace, tokenSecretName, tokenSecretTokenKey)
	}
	ca, ok := tokenSecret.Data[tokenSecretCAKey]
	if !ok || len(ca) == 0 {
		return "", xerrors.Errorf("token secret %s/%s has no %q key yet; wait for Kubernetes to populate the ServiceAccount token", opts.MemberClusterNamespace, tokenSecretName, tokenSecretCAKey)
	}

	kubeconfig, err := buildKubeConfig(opts.MemberClusterName, memberClusterServerURL, opts.MemberClusterNamespace, ca, token)
	if err != nil {
		return "", xerrors.Errorf("building kubeconfig: %w", err)
	}

	credentialSecretName := resourcenames.MemberClusterCredentialSecretName(opts.MemberClusterName)
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialSecretName,
			Namespace: opts.OperatorNamespace,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{credentialSecretKey: string(kubeconfig)},
	}

	memberCluster := &operatorv1.MemberCluster{
		TypeMeta: metav1.TypeMeta{APIVersion: operatorv1.GroupVersion.String(), Kind: "MemberCluster"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.MemberClusterName,
			Namespace: opts.OperatorNamespace,
		},
		Spec: operatorv1.MemberClusterSpec{
			ClusterName:         opts.MemberClusterLogicalName,
			CredentialSecretRef: corev1.LocalObjectReference{Name: credentialSecretName},
		},
	}

	secretYAML, err := yaml.Marshal(secret)
	if err != nil {
		return "", xerrors.Errorf("marshalling credential secret: %w", err)
	}
	memberClusterYAML, err := yaml.Marshal(memberCluster)
	if err != nil {
		return "", xerrors.Errorf("marshalling MemberCluster CR: %w", err)
	}

	return string(secretYAML) + "---\n" + string(memberClusterYAML), nil
}

// buildKubeConfig returns a serialised single-context kubeconfig with bearer-token auth.
func buildKubeConfig(clusterName, serverURL, namespace string, ca, token []byte) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   serverURL,
		CertificateAuthorityData: ca,
	}
	cfg.AuthInfos["mck-operator"] = &clientcmdapi.AuthInfo{
		Token: string(token),
	}
	cfg.Contexts[clusterName] = &clientcmdapi.Context{
		Cluster:   clusterName,
		AuthInfo:  "mck-operator",
		Namespace: namespace,
	}
	cfg.CurrentContext = clusterName
	return clientcmd.Write(*cfg)
}
