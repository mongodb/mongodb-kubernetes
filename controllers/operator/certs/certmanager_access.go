package certs

import (
	"context"
	"os"
	"strconv"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// CertManagerReady reports whether this operator can use cert-manager to manage the certificates.
// It would mean cert-manager resources are registered, helm field is enabled (that results in RBAC
// getting installed) and operator actually has permissions to manage cert-manager resources.
// It is evaluated once at startup. The caller uses it both to decide whether to watch Certificates
// and to fail a resource that asks for security.managedCertificate, so that a missing permission
// never reaches cert-manager.
// watchedNamespaces are the namespaces the operator watches, as returned by GetWatchedNamespace:
// []string{""} when it watches the whole cluster, otherwise the list of namespaces.
func CertManagerReady(ctx context.Context, c kubernetesClient.Client, restMapper apimeta.RESTMapper, watchedNamespaces []string) bool {
	if !CertManagerCRDsInstalled(restMapper) {
		return false
	}
	if !ManagedCertificateRBACEnabled() {
		return false
	}
	return certManagerAccessAllowed(ctx, c, watchedNamespaces)
}

// ManagedCertificateRBACEnabled reports whether the chart installed the cert-manager related RBAC.
// helm installs RBACs when security.managedCertificate.enabled helm field is set to true.
func ManagedCertificateRBACEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv(util.ManagedCertificateEnv))
	return err == nil && enabled
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
// Certificates and Issuers. This call doesn't need any new RBAC to work.
//
// The question is asked in every watched namespace, because that is where the RBAC is granted. When
// the operator watches a single namespace (the default install) or a list of them, helm installs a
// namespaced Role bound in each one, and asking without a namespace would mean "may I do this in
// every namespace?", which a namespaced Role never allows. When it watches the whole cluster the
// namespace is "", and helm installs a ClusterRole, so the cluster-wide question is the right one.
func certManagerAccessAllowed(ctx context.Context, c kubernetesClient.Client, watchedNamespaces []string) bool {
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
					return false
				}
				if !review.Status.Allowed {
					return false
				}
			}
		}
	}
	return true
}
