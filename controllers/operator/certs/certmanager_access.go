package certs

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// CertManagerReadiness is the result of the startup check: whether the operator may use
// cert-manager, and the reason to report on a MongoDB or other resources when it may not.
type CertManagerReadiness struct {
	// Ready is true when the operator may use cert-manager.
	Ready bool
	// NotReadyReason says what is missing when Ready is false. It is empty when Ready is true.
	NotReadyReason string
}

// CertManagerReady reports whether this operator can use cert-manager to manage the certificates:
// the cert-manager resources have to be registered in the cluster, and the operator should have appropriate
// RBAC to manage those certificates. When it cannot, the returned reason says what is missing, so that a
// resource asking for security.managedCertificate can report it.
// It is evaluated once at startup. The caller uses it both to decide whether to watch Certificates
// and to fail a resource that asks for security.managedCertificate, so that a missing permission
// never reaches cert-manager.
// watchedNamespaces are the namespaces the operator watches, as returned by GetWatchedNamespace:
// []string{""} when it watches the whole cluster, otherwise the list of namespaces.
func CertManagerReady(ctx context.Context, c kubernetesClient.Client, restMapper apimeta.RESTMapper, watchedNamespaces []string) CertManagerReadiness {
	if !CertManagerCRDsInstalled(restMapper) {
		return CertManagerReadiness{NotReadyReason: "It seems cert-manager is not installed. Install it, grant the operator the cert-manager RBAC (e.g. Helm value security.managedCertificate.enabled=true), then restart the operator."}
	}

	if allowed, reason := certManagerAccessAllowed(ctx, c, watchedNamespaces); !allowed {
		return CertManagerReadiness{NotReadyReason: reason}
	}
	return CertManagerReadiness{Ready: true}
}

func CertManagerCRDsInstalled(restMapper apimeta.RESTMapper) bool {
	gvk := schema.GroupVersionKind{
		Group:   certmanagerv1.SchemeGroupVersion.Group,
		Version: certmanagerv1.SchemeGroupVersion.Version,
		Kind:    "Certificate",
	}
	_, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

// certManagerAccessAllowed asks the API server whether this operator may use cert-manager
// Certificates and Issuers, and returns the reason when it may not. This call doesn't need any new
// RBAC to work.
//
// The question is asked in every watched namespace, because that is where the RBAC is granted. When
// the operator watches a single namespace (the default install) or a list of them, helm installs a
// namespaced Role bound in each one, and asking without a namespace would mean "may I do this in
// every namespace?", which a namespaced Role never allows. When it watches the whole cluster the
// namespace is "", and helm installs a ClusterRole, so the cluster-wide question is the right one.
func certManagerAccessAllowed(ctx context.Context, c kubernetesClient.Client, watchedNamespaces []string) (bool, string) {
	verbs := []string{"get", "list", "watch", "create", "update"}
	for _, namespace := range watchedNamespaces {
		for _, resource := range []string{"certificates", "issuers"} {
			for _, verb := range verbs {
				review := &authorizationv1.SelfSubjectAccessReview{
					Spec: authorizationv1.SelfSubjectAccessReviewSpec{
						ResourceAttributes: &authorizationv1.ResourceAttributes{
							Namespace: namespace,
							Group:     certmanagerv1.SchemeGroupVersion.Group,
							Resource:  resource,
							Verb:      verb,
						},
					},
				}
				if err := c.Create(ctx, review); err != nil {
					return false, fmt.Sprintf("the operator could not check whether it may manage cert-manager resources: %v", err)
				}
				if !review.Status.Allowed {
					return false, fmt.Sprintf("the operator is not allowed to %s cert-manager %s in %s. Grant it the cert-manager RBAC (e.g. Helm value security.managedCertificate.enabled=true), then restart the operator.", verb, resource, describeNamespace(namespace))
				}
			}
		}
	}
	return true, ""
}

// UnavailableMessage is what a resource that enables security.managedCertificate is told when the
// operator cannot use cert-manager. The reason is empty in tests that build a reconciler with the
// zero value, so the sentence then names both possible causes.
func (r CertManagerReadiness) UnavailableMessage() string {
	reason := r.NotReadyReason
	if reason == "" {
		reason = "cert-manager is not installed in the cluster, or the operator is not allowed to manage cert-manager resources"
	}
	return fmt.Sprintf("security.managedCertificate is enabled, but this operator cannot use cert-manager: %s", reason)
}

// describeNamespace renders the namespace a permission check ran in for a user facing message.
func describeNamespace(namespace string) string {
	if namespace == "" {
		return "the cluster"
	}
	return fmt.Sprintf("namespace %s", namespace)
}
