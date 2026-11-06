package certs

import (
	"context"
	"slices"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// ClientCertificate builds the cert-manager Certificate that holds a MongoDBUser's x509 client
// certificate, for an application to connect to a resource that uses operator-managed certificates.
// The subject comes from the user (MongoDBUser.ClientCertificateSubject), so the certificate always
// carries the same subject the user is registered under.
//
// It is signed by whichever CA mongod checks client certificates against. The operator sets
// clusterCAFile (the client CA) only when internal cluster authentication or the agent uses x509;
// otherwise mongod checks client certificates against CAFile (the server CA):
//
//	internal cluster auth or agent is x509 -> client signer (client.issuerRef, else the operator managed client CA)
//	otherwise                              -> server signer (server.issuerRef, else the operator's managed server CA)
//
// mongod treats any client whose certificate has the same O and OU as the membership certificate
// as a cluster member, so the subject must differ from the membership subject in O or OU.
func ClientCertificate(res CertificateOwner, user *userv1.MongoDBUser) (*certmanagerv1.Certificate, error) {
	sec := res.GetSecurity()
	if !sec.IsManagedCertificateEnabled() {
		return nil, xerrors.Errorf("%s does not use operator-managed certificates (spec.security.managedCertificate is not set), so the operator cannot issue a client certificate for it", res.GetName())
	}
	if !sec.Authentication.IsX509Enabled() {
		return nil, xerrors.Errorf("%s does not use x509 authentication, so a client certificate cannot be used to log in to it", res.GetName())
	}

	sub := user.ClientCertificateSubject()
	clientCertSub := &certmanagerv1.X509Subject{
		Organizations:       sub.Organizations,
		OrganizationalUnits: sub.OrganizationalUnits,
	}

	membership, _ := membershipSubject(res)
	if slices.Equal(clientCertSub.Organizations, membership.Organizations) && slices.Equal(clientCertSub.OrganizationalUnits, membership.OrganizationalUnits) {
		return nil, xerrors.Errorf("the client certificate subject must differ from the membership subject in organizations or organizationalUnits: both are O=%v, OU=%v, so mongod would treat the client as a cluster member", clientCertSub.Organizations, clientCertSub.OrganizationalUnits)
	}

	signer := categoryServer
	if sec.RequiresX509ClientCerts() {
		signer = categoryClient
	}

	clientCert := &certmanagerv1.Certificate{}
	clientCert.Name = user.ClientCertificateName()
	clientCert.Namespace = res.GetNamespace()
	clientCert.Spec = certSpec(res, signer, user.ClientCertificateSecretName(), nil, sub.CommonName, clientCertSub, clientAuthCertUsages)
	return clientCert, nil
}

// EnsureClientCertificate creates or updates a user's client Certificate and reports whether
// cert-manager has issued it. ownerRefs are set on the Certificate so it is deleted with the user.
// It returns Pending until the certificate is ready, with the issuance error when cert-manager
// reports one.
func EnsureClientCertificate(ctx context.Context, c kubernetesClient.Client, clientCert *certmanagerv1.Certificate, ownerRefs []metav1.OwnerReference) workflow.Status {
	existing := &certmanagerv1.Certificate{}
	err := c.Get(ctx, types.NamespacedName{Name: clientCert.Name, Namespace: clientCert.Namespace}, existing)
	switch {
	case err == nil:
		existing.Spec = clientCert.Spec
		existing.OwnerReferences = ownerRefs
		if err := c.Update(ctx, existing); err != nil {
			return workflow.Failed(err)
		}
	case meta.IsNoMatchError(err):
		return workflow.Failed(xerrors.Errorf("cert-manager is required for operator-managed certificates but its CRDs are not installed in the cluster"))
	case apiErrors.IsNotFound(err):
		desired := clientCert.DeepCopy()
		desired.OwnerReferences = ownerRefs
		if err := c.Create(ctx, desired); err != nil {
			return workflow.Failed(err)
		}
	default:
		return workflow.Failed(err)
	}

	insp, status := inspectCertificate(ctx, c, clientCert.Namespace, clientCert.Name)
	if !status.IsOK() {
		return status
	}
	if !insp.ready {
		if insp.issuanceFailing {
			return workflow.Pending("Waiting for cert-manager to issue the client certificate: %s", insp.detail)
		}
		return workflow.Pending("Waiting for cert-manager to issue the client certificate %s", clientCert.Name)
	}
	return workflow.OK()
}
