// Package memberregistration produces the registration a member cluster needs so the MCK
// operator can reach it: a credential Secret (a single-context kubeconfig) and a MemberCluster
// CR referencing that Secret. It reads the token of the user-provisioned member ServiceAccount
// (found via the kubernetes.io/service-account.name annotation on the ServiceAccount token
// Secrets in the member namespace) and writes both resources as a multi-document YAML string.
// It holds the logic; the CLI wiring lives in cmd/kubectl-mongodb.
package memberregistration

import (
	"context"
	"strings"
	"time"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/fields"
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
	// MemberClusterName is the RFC 1123 name used for the central-cluster resources this
	// registration emits: the MemberCluster CR's metadata.name and the credential Secret name
	// suffix. It must be unique per member cluster on the central cluster.
	MemberClusterName string
	// MemberClusterLogicalName is the logical cluster identity set as spec.clusterName on the MemberCluster CR.
	// Used to resolve clusterSpecList[].clusterName references in workload CRs.
	MemberClusterLogicalName string
	// MemberClusterNamespace is the namespace on the member cluster holding the operator's
	// credentials (the member ServiceAccount and its token Secret).
	MemberClusterNamespace string
	// MemberClusterServiceAccount is the name of the user-provisioned ServiceAccount on the
	// member cluster the operator authenticates as. Its token Secret is discovered via the
	// kubernetes.io/service-account.name annotation. Must match the ServiceAccount the
	// member-cluster RBAC is bound to (--member-cluster-service-account of
	// generate-member-resources).
	MemberClusterServiceAccount string
	// OperatorNamespace is the namespace on the operator's cluster where the emitted CR and
	// credential Secret are placed.
	OperatorNamespace        string
	MemberClusterApiServer   string
	MemberClusterApiServerCA []byte
	// TokenWaitTimeout is how long Generate waits for the token Secret's data keys to be
	// populated by Kubernetes's token controller before failing. It must be positive; the
	// CLI passes DefaultTokenWaitTimeout. Only the data keys are awaited: the Secret itself
	// must already exist (the user creates it together with the ServiceAccount).
	TokenWaitTimeout time.Duration
}

// Generate reads the member ServiceAccount token Secret via client and builds the output using
// serverURL as the kubeconfig API-server address. It is the entry point used by the CLI, which
// builds client and serverURL from the member cluster's kubeconfig context.
func Generate(ctx context.Context, memberClusterClient kubernetes.Interface, memberClusterServerURL string, opts Options) (string, error) {
	if strings.TrimSpace(opts.MemberClusterServiceAccount) == "" {
		return "", xerrors.Errorf("MemberClusterServiceAccount must be non-empty")
	}

	if err := ensureServiceAccountExists(ctx, memberClusterClient, opts); err != nil {
		return "", err
	}

	tokenSecretName, err := findTokenSecret(ctx, memberClusterClient, opts)
	if err != nil {
		return "", err
	}

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

// ensureServiceAccountExists verifies the user-provisioned member ServiceAccount exists, so a
// missing credential fails fast with a clear message instead of surfacing as a token-Secret
// lookup failure.
func ensureServiceAccountExists(ctx context.Context, memberClusterClient kubernetes.Interface, opts Options) error {
	_, err := memberClusterClient.CoreV1().ServiceAccounts(opts.MemberClusterNamespace).Get(ctx, opts.MemberClusterServiceAccount, metav1.GetOptions{})
	if err != nil {
		return xerrors.Errorf("reading ServiceAccount %s/%s on the member cluster (was the credentials manifest applied to it?): %v", opts.MemberClusterNamespace, opts.MemberClusterServiceAccount, err)
	}
	return nil
}

// findTokenSecret locates the token Secret belonging to the member ServiceAccount: a Secret of
// type kubernetes.io/service-account-token annotated with the ServiceAccount's name
// (kubernetes.io/service-account.name — the annotation the token controller requires to
// populate a token Secret). Zero matches means the user has not created the Secret — an
// immediate error, not a wait; multiple matches are ambiguous and refused.
func findTokenSecret(ctx context.Context, memberClusterClient kubernetes.Interface, opts Options) (string, error) {
	secrets, err := memberClusterClient.CoreV1().Secrets(opts.MemberClusterNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("type", string(corev1.SecretTypeServiceAccountToken)).String(),
	})
	if err != nil {
		return "", xerrors.Errorf("listing token secrets in %s on the member cluster: %v", opts.MemberClusterNamespace, err)
	}

	var matches []string
	for _, secret := range secrets.Items {
		// The type is re-checked in code (not just via the field selector above): fake
		// clientsets used in tests discard field selectors, and only the type excludes a
		// non-token Secret that happens to carry the same ServiceAccount annotation.
		if secret.Type != corev1.SecretTypeServiceAccountToken {
			continue
		}
		if secret.Annotations[corev1.ServiceAccountNameKey] == opts.MemberClusterServiceAccount {
			matches = append(matches, secret.Name)
		}
	}

	switch len(matches) {
	case 0:
		return "", xerrors.Errorf("no token Secret for ServiceAccount %q found in namespace %s on the member cluster: create a Secret of type %s annotated with %s: %s",
			opts.MemberClusterServiceAccount, opts.MemberClusterNamespace, corev1.SecretTypeServiceAccountToken, corev1.ServiceAccountNameKey, opts.MemberClusterServiceAccount)
	case 1:
		return matches[0], nil
	default:
		return "", xerrors.Errorf("found %d token Secrets for ServiceAccount %q in namespace %s on the member cluster (%s); exactly one is expected — delete the extras",
			len(matches), opts.MemberClusterServiceAccount, opts.MemberClusterNamespace, strings.Join(matches, ", "))
	}
}

// readTokenSecret Gets the token Secret once and returns the complete, user-facing error for a
// failed read: a Get error (including NotFound) or a missing/empty token or ca.crt data key.
// waitForTokenSecret's poll condition uses the error verbatim.
func readTokenSecret(ctx context.Context, memberClusterClient kubernetes.Interface, namespace, name string) (token, ca []byte, err error) {
	secret, err := memberClusterClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, xerrors.Errorf("reading token secret %s/%s on the member cluster: %v", namespace, name, err)
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
// ca.crt data keys or opts.TokenWaitTimeout elapses. The Secret is known to exist by this point
// (findTokenSecret located it); the poll covers the token controller's population delay, plus
// transient API errors and a mid-wait deletion, none of which should abort the wait.
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
