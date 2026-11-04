package certs

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// ServerCertificateOptions describes a server-only certificate: the naming base for the
// operator-owned CA chain, the leaf Secret, the explicitly computed SANs and the CA bundle
// ConfigMap destination.
type ServerCertificateOptions struct {
	NamingBase   string
	SecretName   string
	DNSNames     []string
	IPAddresses  []string
	CABundleName string
	CABundleKey  string
}

// serverCertificateOwner presents a server-only Ops Manager component to the shared issuance
// helpers: certificates are named after the naming base and owned by the Ops Manager resource.
// Its security only carries the server category, so no client certificates are ever issued.
type serverCertificateOwner struct {
	v1.ObjectOwner
	name     string
	security *mdbv1.Security
}

func (o serverCertificateOwner) GetName() string               { return o.name }
func (o serverCertificateOwner) GetSecurity() *mdbv1.Security  { return o.security }
func (o serverCertificateOwner) ownerResource() v1.ObjectOwner { return o.ObjectOwner }

// EnsureServerCertificateAndCA issues the serverAuth certificate for an Ops Manager
// component that has no client category (the app server and the queryable backup daemon).
// The certificate and CA bundle are owned by owner; generated names derive from
// opts.NamingBase so the components cannot collide. Callers compute the SANs. The returned
// bool reports that a requested prune of the previous server CA has been applied and the
// caller may remove the prune annotation.
func EnsureServerCertificateAndCA(
	ctx context.Context,
	c kubernetesClient.Client,
	owner v1.ObjectOwner,
	cfg *omv1.ManagedCertificate,
	opts ServerCertificateOptions,
	log *zap.SugaredLogger,
) (workflow.Status, metav1.Condition, bool) {
	if cfg == nil {
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "Operator-managed certificates are not required for this resource"), false
	}
	if opts.NamingBase == "" || opts.SecretName == "" {
		msg := "operator-managed server certificate requires a naming base and a secret name"
		return workflow.Failed(xerrors.New(msg)), issuanceFailedCondition(msg), false
	}
	if opts.CABundleName == "" || opts.CABundleKey == "" {
		msg := "operator-managed server certificate requires a CA bundle ConfigMap name and key"
		return workflow.Failed(xerrors.New(msg)), issuanceFailedCondition(msg), false
	}

	res := serverCertificateOwner{
		ObjectOwner: owner,
		name:        opts.NamingBase,
		security: &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{
			Server:   &cfg.CertificateConfig,
			Subjects: &mdbv1.CertSubjects{Server: cfg.Subject},
		}},
	}
	subject, commonName := serverSubject(res)
	spec := certSpec(res, categoryServer, opts.SecretName, opts.DNSNames, commonName, subject, memberServerCertUsages)
	spec.IPAddresses = opts.IPAddresses

	renewalFailures, status, condition := ensureCertificatesReady(ctx, c, res, []certToEnsure{{certName: opts.SecretName, spec: spec}}, false)
	if !status.IsOK() {
		return status, condition, false
	}

	previousRetained, err := ensureCategoryBundle(ctx, c, res, categoryServer, []Options{{CertSecretName: opts.SecretName}},
		caBundleTarget{name: opts.CABundleName, aliasKey: opts.CABundleKey})
	if err != nil {
		return workflow.Failed(err), issuanceFailedCondition(err.Error()), false
	}
	log.Debugf("Populated operator-managed CA ConfigMap %s for %s", opts.CABundleName, opts.NamingBase)

	refused := pruneRequested(res, categoryServer) && previousRetained
	status, condition = certificatesResult(renewalFailures, previousCASentence(categoryServer, previousRetained, refused))
	return status, condition, pruneRequested(res, categoryServer) && !refused
}
