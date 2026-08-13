package membercluster

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// Reasons for the RBACValid status condition on MemberCluster.
const (
	reasonValid                        = "Valid"
	reasonValidationDisabled           = "ValidationDisabled"
	reasonVersionMismatch              = "VersionMismatch"
	reasonRBACVersionMissing           = "RBACVersionMissing"
	reasonMemberServiceAccountNotFound = "MemberServiceAccountNotFound"
	reasonProbeForbidden               = "ProbeForbidden"
	reasonCredentialSecretMissing      = "CredentialSecretMissing"
	reasonCredentialInvalid            = "CredentialInvalid"          //nolint:gosec // condition reason string, not a credential
	reasonCredentialNamespaceMissing   = "CredentialNamespaceMissing" //nolint:gosec // condition reason string, not a credential
	reasonProbeFailed                  = "ProbeFailed"
)

// probeOutcome carries the RBACValid condition status/reason/message derived from probing
// the member cluster's operator ServiceAccount.
type probeOutcome struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

// rbacValidator probes a member cluster's RBAC by reading the operator's ServiceAccount
// through the cluster's credential rest.Config and comparing its rbac-version annotation
// with the operator's expected version.
type rbacValidator struct {
	// newClient builds a direct (uncached) client for the member cluster; the probe must
	// not go through the provider's cached clients, which the validation outcome gates.
	newClient func(restConfig *restclient.Config) (client.Client, error)
}

func newRBACValidator() rbacValidator {
	return rbacValidator{newClient: func(restConfig *restclient.Config) (client.Client, error) {
		return client.New(restConfig, client.Options{Scheme: clientsetscheme.Scheme})
	}}
}

// Probe reads the member cluster's operator ServiceAccount and classifies the result into
// an RBACValid condition outcome. Transient failures (network, timeout, 5xx) yield Unknown
// so the caller keeps the provider entry; definitive problems yield False.
func (v rbacValidator) Probe(ctx context.Context, restConfig *restclient.Config, saName, saNamespace, expectedVersion string) probeOutcome {
	c, err := v.newClient(restConfig)
	if err != nil {
		return probeOutcome{metav1.ConditionUnknown, reasonProbeFailed, fmt.Sprintf("Failed to build a client for the member cluster: %v", err)}
	}

	sa := &corev1.ServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Name: saName, Namespace: saNamespace}, sa); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return probeOutcome{metav1.ConditionFalse, reasonMemberServiceAccountNotFound, fmt.Sprintf(
				"ServiceAccount %q not found in namespace %q of the member cluster. Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.", saName, saNamespace)}
		case apierrors.IsForbidden(err):
			return probeOutcome{metav1.ConditionFalse, reasonProbeForbidden, fmt.Sprintf(
				"The member cluster denied reading ServiceAccount %q in namespace %q: the operator's member ServiceAccount lacks the 'serviceaccounts get' permission. Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.", saName, saNamespace)}
		case apierrors.IsUnauthorized(err):
			return probeOutcome{metav1.ConditionFalse, reasonCredentialInvalid, "The member cluster rejected the operator's credentials (unauthorized); the token was likely revoked. Regenerate credentials with 'kubectl mongodb multicluster generate-member-registration'."}
		default:
			return probeOutcome{metav1.ConditionUnknown, reasonProbeFailed, fmt.Sprintf(
				"Failed to read ServiceAccount %q in namespace %q from the member cluster: %v", saName, saNamespace, err)}
		}
	}

	version, ok := sa.Annotations[util.MemberClusterRBACVersionAnnotation]
	if !ok {
		return probeOutcome{metav1.ConditionFalse, reasonRBACVersionMissing, fmt.Sprintf(
			"ServiceAccount %q in namespace %q has no %q annotation. Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.", saName, saNamespace, util.MemberClusterRBACVersionAnnotation)}
	}
	if version != expectedVersion {
		return probeOutcome{metav1.ConditionFalse, reasonVersionMismatch, fmt.Sprintf(
			"RBAC version %q on the member cluster does not match the operator's expected version %q. Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.", version, expectedVersion)}
	}
	return probeOutcome{metav1.ConditionTrue, reasonValid, fmt.Sprintf(
		"RBAC version %q matches the operator's expected version.", version)}
}
