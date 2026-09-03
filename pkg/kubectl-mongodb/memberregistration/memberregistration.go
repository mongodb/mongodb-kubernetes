// Package memberregistration produces the registration a member cluster needs so the MCK
// operator can reach it: a credential Secret (a single-context kubeconfig) and a MemberCluster
// CR referencing that Secret. It reads the ServiceAccount token that
// `generate-member-resources` created on the member cluster and writes both resources as a
// multi-document YAML string. It holds the logic; the CLI wiring lives in cmd/kubectl-mongodb.
package memberregistration

import (
	"context"
	"time"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// DefaultTokenWaitTimeout is the default budget the CLI gives Kubernetes's token controller
	// to populate the ServiceAccount token Secret before Generate fails.
	DefaultTokenWaitTimeout = time.Minute
)

// tokenPollInterval is how often Generate re-reads the token Secret while waiting for Kubernetes
// to populate it. It is a var, not a const, so tests in this package can shorten it (restore with defer).
var tokenPollInterval = 2 * time.Second

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
	OperatorNamespace        string
	MemberClusterApiServer   string
	MemberClusterApiServerCA []byte
	// TokenWaitTimeout is how long Generate waits for the token Secret to be populated by
	// Kubernetes's token controller before failing. It must be positive; the CLI passes
	// DefaultTokenWaitTimeout.
	TokenWaitTimeout time.Duration
}

// Generate reads the member ServiceAccount token Secret via client and builds the output using
// serverURL as the kubeconfig API-server address. It is the entry point used by the CLI, which
// builds client and serverURL from the member cluster's kubeconfig context.
func Generate(ctx context.Context, memberClusterClient kubernetes.Interface, memberClusterServerURL string, opts Options) (string, error) {
	tokenSecretName := resourcenames.MemberClusterTokenSecretName(opts.MemberClusterName)

	token, ca, err := waitForTokenSecret(ctx, memberClusterClient, tokenSecretName, opts)
	if err != nil {
		return "", err
	}

	if opts.MemberClusterApiServer != "" {
		memberClusterServerURL = opts.MemberClusterApiServer
	}
	if len(opts.MemberClusterApiServerCA) > 0 {
		ca = opts.MemberClusterApiServerCA
	}

	kubeconfig, err := buildKubeConfig(opts.MemberClusterName, memberClusterServerURL, opts.MemberClusterNamespace, ca, token)
	if err != nil {
		return "", xerrors.Errorf("building kubeconfig: %v", err)
	}

	credentialSecretName := resourcenames.MemberClusterCredentialSecretName(opts.MemberClusterName)
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialSecretName,
			Namespace: opts.OperatorNamespace,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{util.MemberClusterCredentialSecretKubeconfigKey: string(kubeconfig)},
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
		return "", xerrors.Errorf("marshalling credential secret: %v", err)
	}
	memberClusterYAML, err := yaml.Marshal(memberCluster)
	if err != nil {
		return "", xerrors.Errorf("marshalling MemberCluster CR: %v", err)
	}

	return string(secretYAML) + "---\n" + string(memberClusterYAML), nil
}

// readTokenSecret Gets the token Secret once and returns the complete, user-facing error for a
// failed read: a Get error (including NotFound) or a missing/empty token or ca.crt data key.
// waitForTokenSecret's poll condition uses the error verbatim.
func readTokenSecret(ctx context.Context, memberClusterClient kubernetes.Interface, namespace, name string) (token, ca []byte, err error) {
	secret, err := memberClusterClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, xerrors.Errorf("reading token secret %s/%s on the member cluster (was 'generate-member-resources' applied to it?): %v", namespace, name, err)
	}
	if token = secret.Data[corev1.ServiceAccountTokenKey]; len(token) == 0 {
		return nil, nil, xerrors.Errorf("token secret %s/%s has no %q key yet; wait for Kubernetes to populate the ServiceAccount token", namespace, name, corev1.ServiceAccountTokenKey)
	}
	if ca = secret.Data[corev1.ServiceAccountRootCAKey]; len(ca) == 0 {
		return nil, nil, xerrors.Errorf("token secret %s/%s has no %q key yet; wait for Kubernetes to populate the ServiceAccount token", namespace, name, corev1.ServiceAccountRootCAKey)
	}
	return token, ca, nil
}

// waitForTokenSecret polls the token Secret until Kubernetes has populated both its token and
// ca.crt data keys or opts.TokenWaitTimeout elapses. It retries on all errors: the Secret may
// not exist yet, and transient API errors should not abort the wait.
func waitForTokenSecret(ctx context.Context, memberClusterClient kubernetes.Interface, tokenSecretName string, opts Options) ([]byte, []byte, error) {
	var token, ca []byte
	var lastErr error
	cond := func(ctx context.Context) (bool, error) {
		var err error
		token, ca, err = readTokenSecret(ctx, memberClusterClient, opts.MemberClusterNamespace, tokenSecretName)
		lastErr = err
		return err == nil, nil
	}

	if err := wait.PollUntilContextTimeout(ctx, tokenPollInterval, opts.TokenWaitTimeout, true, cond); err != nil {
		return nil, nil, xerrors.Errorf("timed out after %s waiting for the member ServiceAccount token: %v", opts.TokenWaitTimeout, lastErr)
	}
	return token, ca, nil
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
