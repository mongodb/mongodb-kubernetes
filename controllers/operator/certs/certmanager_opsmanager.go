package certs

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
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
	// PruneTarget is the Ops Manager prune target of this certificate (OpsManagerPruneTargetOM,
	// OpsManagerPruneTargetAppDB or OpsManagerPruneTargetBackup). Empty keeps the category-based
	// annotation of the mongod-family resources.
	PruneTarget string
	// OnRenewals is optional: when set, it receives the component's next automatic certificate
	// renewal.
	OnRenewals func(*mdbstatus.CertificateRenewalStatus)
}

// serverCertificateOwner presents a server-only Ops Manager component to the shared issuance
// helpers: certificates are named after the naming base and owned by the Ops Manager resource.
// Its security only carries the server category, so no client certificates are ever issued.
type serverCertificateOwner struct {
	v1.ObjectOwner
	name             string
	pruneTargetValue string
	security         *mdbv1.Security
	// setRenewals is optional: when set, it receives the component's next automatic certificate
	// renewal; when nil, the renewal is not reported.
	setRenewals func(*mdbstatus.CertificateRenewalStatus)
}

func newServerCertificateOwner(owner v1.ObjectOwner, cfg *omv1.ManagedCertificate, opts ServerCertificateOptions) serverCertificateOwner {
	return serverCertificateOwner{
		ObjectOwner:      owner,
		name:             opts.NamingBase,
		pruneTargetValue: opts.PruneTarget,
		security: &mdbv1.Security{ManagedCertificate: &mdbv1.ManagedCertificate{
			Server: &cfg.CertificateConfig,
		}},
		setRenewals: opts.OnRenewals,
	}
}

func (o serverCertificateOwner) GetName() string               { return o.name }
func (o serverCertificateOwner) GetSecurity() *mdbv1.Security  { return o.security }
func (o serverCertificateOwner) ownerResource() v1.ObjectOwner { return o.ObjectOwner }
func (o serverCertificateOwner) pruneTarget() string           { return o.pruneTargetValue }

func (o serverCertificateOwner) SetCertificateRenewals(renewals *mdbstatus.CertificateRenewalStatus) {
	if o.setRenewals != nil {
		o.setRenewals(renewals)
	}
}

// serverSubject is the optional subject for server-only certificates (the Ops Manager app
// server and queryable backup daemon). Default is SAN-only (nil subject); a user override
// (spec.security.managedCertificate.subject) supplies a DN to satisfy a server issuer's policy.
func serverSubject(override *mdbv1.X509Subject) (*certmanagerv1.X509Subject, string) {
	if override == nil {
		return nil, ""
	}
	return mergeSubject(override, &certmanagerv1.X509Subject{}, "")
}

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
		return workflow.OK(), CertificatesReadyCondition(metav1.ConditionTrue, ReasonAllCertificatesReady, "Operator-managed certificates are not required for this resource"), false
	}
	if opts.NamingBase == "" || opts.SecretName == "" {
		msg := "operator-managed server certificate requires a naming base and a secret name"
		return workflow.Failed(xerrors.New(msg)), IssuanceFailedCondition(msg), false
	}
	if opts.CABundleName == "" || opts.CABundleKey == "" {
		msg := "operator-managed server certificate requires a CA bundle ConfigMap name and key"
		return workflow.Failed(xerrors.New(msg)), IssuanceFailedCondition(msg), false
	}

	res := newServerCertificateOwner(owner, cfg, opts)
	subject, commonName := serverSubject(cfg.Subject)
	spec := certSpec(res, categoryServer, opts.SecretName, opts.DNSNames, commonName, subject, memberServerCertUsages)
	spec.IPAddresses = opts.IPAddresses

	renewalFailures, status, condition := ensureCertificatesReady(ctx, c, res, []certToEnsure{{certName: opts.SecretName, category: categoryServer, spec: spec}}, false)
	if !status.IsOK() {
		return status, condition, false
	}

	previousRetained, err := ensureCategoryBundle(ctx, c, res, categoryServer, []Options{{CertSecretName: opts.SecretName}},
		caBundleTarget{name: opts.CABundleName, aliasKey: opts.CABundleKey})
	if err != nil {
		return workflow.Failed(err), IssuanceFailedCondition(err.Error()), false
	}
	log.Debugf("Populated operator-managed CA ConfigMap %s for %s", opts.CABundleName, opts.NamingBase)

	refused := pruneRequested(res, categoryServer) && previousRetained
	status, condition = certificatesResult(renewalFailures, previousCASentence(res, categoryServer, previousRetained, refused))
	return status, condition, pruneRequested(res, categoryServer) && !refused
}
