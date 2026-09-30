package certs

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// ConditionCertificatesReady is the status condition type reporting the aggregate
	// health of the operator-managed cert-manager Certificates for a resource. It is
	// tracked independently of the top-level Phase. A failing renewal sets it False
	// while the deployment keeps running on the still-valid current cert (Phase stays
	// Running).
	ConditionCertificatesReady = "CertificatesReady"

	// every managed cert is issued and up to date.
	reasonAllCertificatesReady = "AllCertificatesReady"
	// a usable cert is not yet available this also holds overall Phase in Pending.
	reasonCertificatesIssuing = "Issuing"
	// cert-manager returned an API/CRD error.
	reasonCertificateIssuanceError = "IssuanceFailed"
	// a renewal is failing but the current cert is still valid, so the deployment keeps running.
	reasonCertificateRenewalFailed = "RenewalFailed"
)

// The certificates that are required by a deployment can be grouped in two categories, server certs
// and client certs. It's a type because entire API revolves around these categories, we accept
// issuers in CR based on the category, server or client. And those issuers sign the respective
// certificates.
type certCategory string

const (
	categoryServer certCategory = "server"
	categoryClient certCategory = "client"
)

// CertificateOwner is the minimal view of a mongod-family CR (MongoDB, MongoDBMultiCluster,
// AppDB) that owns the operator-issued cert-manager Certificate(s).
type CertificateOwner interface {
	// ObjectOwner gives GetName/GetNamespace/GetUID/GetKind (for ownerRefs).
	v1.ObjectOwner
	// GetSecurity exposes security.managedCertificate, internal-cluster/agent auth
	// modes, the derived secret names and the CA ConfigMap name.
	GetSecurity() *mdbv1.Security
}

var memberServerCertUsages = []certmanagerv1.KeyUsage{
	certmanagerv1.UsageDigitalSignature,
	certmanagerv1.UsageKeyEncipherment,
	certmanagerv1.UsageServerAuth,
}

var clientAuthCertUsages = []certmanagerv1.KeyUsage{
	certmanagerv1.UsageDigitalSignature,
	certmanagerv1.UsageKeyEncipherment,
	certmanagerv1.UsageClientAuth,
}

// certToEnsure describes one cert-manager Certificate the operator must ensure
// for a resource.
type certToEnsure struct {
	certName    string
	spec        certmanagerv1.CertificateSpec
	annotations map[string]string
}

// coveredMembersAnnotation stored how many members' SANs are currently baked
// into the operator-managed member/server certificate. It lets a later reconcile hold the
// SAN list steady while the scale fo deployment is in progress so the shared cert is not
// reissued at every one-at-a-time step.
const coveredMembersAnnotation = "mongodb.com/covered-members"

func selfSignedIssuerName(res CertificateOwner, cat certCategory) string {
	return fmt.Sprintf("%s-%s-selfsigned", res.GetName(), cat)
}

func managedCACertName(res CertificateOwner, cat certCategory) string {
	return fmt.Sprintf("%s-%s-ca", res.GetName(), cat)
}

func managedCAIssuerName(res CertificateOwner, cat certCategory) string {
	return fmt.Sprintf("%s-%s-ca-issuer", res.GetName(), cat)
}

// certCategoryConfig returns the per-category managedCertificate block (server or client)
func certCategoryConfig(res CertificateOwner, cat certCategory) *mdbv1.CertificateConfig {
	cm := res.GetSecurity().ManagedCertificate
	if cm == nil {
		return nil
	}
	if cat == categoryServer {
		return cm.Server
	}
	return cm.Client
}

func hasUserIssuer(res CertificateOwner, cat certCategory) bool {
	cfg := certCategoryConfig(res, cat)
	return cfg != nil && cfg.IssuerRef != nil && cfg.IssuerRef.Name != ""
}

// signerIssuerRef resolves who signs a category's certs: the user-configured issuer when
// present, else the operator's self-signed CA issuer for that category.
func signerIssuerRef(res CertificateOwner, cat certCategory) cmmeta.ObjectReference {
	if hasUserIssuer(res, cat) {
		cfg := certCategoryConfig(res, cat)
		return cmmeta.ObjectReference{Name: cfg.IssuerRef.Name, Kind: cfg.IssuerRef.Kind}
	}
	return cmmeta.ObjectReference{Name: managedCAIssuerName(res, cat), Kind: certmanagerv1.IssuerKind}
}

// EnsureCertificatesAndCA creates/updates the certificates for the passed cert Options. cert Options
// are passed per component, one for RS and many for sharded deployment (mongos, config server, each shard).
// The certificates are created using cert-manager project and issuer is either user configured
// or operator managed self signed CA if user has not configured. It also waits for all certs to
// be ready and eventually populates the per category (client/server) CA configmap.
func EnsureCertificatesAndCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	allOpts []Options,
	log *zap.SugaredLogger,
) (workflow.Status, metav1.Condition) {
	if len(allOpts) == 0 {
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "No operator-managed certificates required")
	}

	// Validate the computed subjects before issuing anything, the agent subject must not match
	// the membership subject on O and OU attributes.
	if status := verifyAgentSubjectDistinct(res); !status.IsOK() {
		return status, issuanceFailedCondition(status.Message())
	}

	// setup the operator managed per category (client/server) self signed CA(s) if it's not
	// configured via user
	hasClientCerts := res.GetSecurity().RequiresX509ClientCerts()

	serverSelfSigned := !hasUserIssuer(res, categoryServer)
	clientSelfSigned := hasClientCerts && !hasUserIssuer(res, categoryClient)
	if serverSelfSigned {
		if status := ensureSelfSignedCA(ctx, c, res, categoryServer); !status.IsOK() {
			return status, issuanceFailedCondition(status.Message())
		}
	}
	if clientSelfSigned {
		if status := ensureSelfSignedCA(ctx, c, res, categoryClient); !status.IsOK() {
			return status, issuanceFailedCondition(status.Message())
		}
	}

	// figure out all the certificates that are required for the reconciling deployment
	var required []certToEnsure
	for _, opts := range allOpts {
		required = append(required, componentCertificates(ctx, c, res, opts)...)
	}
	// agent cert is per deployment not per component (options)
	if agentCert, ok := agentCertificate(res); ok {
		required = append(required, agentCert)
	}

	// Ensure (create/patch) all required certs first, don't wait for certs to
	// be ready immediately after creating them
	for _, cert := range required {
		if status := ensureCertificate(ctx, c, res, cert); !status.IsOK() {
			return status, issuanceFailedCondition(status.Message())
		}
	}

	// Collect names of all the certificates that we should expect to be ready. This would
	// be all the certs in required and two certs that we create for client and server CA.
	names := make([]string, 0, len(required)+2)
	if serverSelfSigned {
		names = append(names, managedCACertName(res, categoryServer))
	}
	if clientSelfSigned {
		names = append(names, managedCACertName(res, categoryClient))
	}
	for _, cert := range required {
		names = append(names, cert.certName)
	}

	// check the status of resources and categorise them. Ones that are not ready yet, ones
	// that are not ready and whose issuance is actively failing (so we can surface the error),
	// and ready ones whose renewal is failing (a soft warning).
	var notReady, issuanceFailures, renewalFailures []string
	for _, name := range names {
		insp, status := inspectCertificate(ctx, c, res.GetNamespace(), name)
		if !status.IsOK() {
			return status, issuanceFailedCondition(status.Message())
		}
		switch {
		case !insp.ready:
			notReady = append(notReady, name)
			if insp.issuanceFailing {
				issuanceFailures = append(issuanceFailures, insp.detail)
			}
		case insp.renewalFailed:
			renewalFailures = append(renewalFailures, insp.detail)
		}
	}

	// Return status to be Pending if any of the certificates are not ready. If issuance is
	// actively failing for some of them, surface that error instead of a generic "waiting"
	// message so a bad issuer is easy to spot.
	if len(notReady) > 0 {
		msg := fmt.Sprintf("Waiting for cert-manager to issue certificate(s): %s.", strings.Join(notReady, ", "))
		if len(issuanceFailures) > 0 {
			msg += fmt.Sprintf(" Issuance is failing for: %s. Check the Certificate and its CertificateRequest for the issuer error.", strings.Join(issuanceFailures, "; "))
		} else {
			msg += " See 'kubectl describe certificate <name>' and its CertificateRequest for issuer detail."
		}
		return workflow.Pending("%s", msg), certificatesReadyCondition(metav1.ConditionFalse, reasonCertificatesIssuing, msg)
	}

	// the member cert (server-signed) and the clusterFile cert (client-signed) must carry
	// the same membership subject
	if status := verifyMembershipSubjects(ctx, c, res, allOpts); !status.IsOK() {
		return status, issuanceFailedCondition(status.Message())
	}

	// All certs Ready, populate the per-category CA trust bundle.
	if status := EnsureCAConfigMapFromIssuedSecret(ctx, c, res, allOpts[0], hasClientCerts, log); !status.IsOK() {
		return status, issuanceFailedCondition(status.Message())
	}

	// the certificates that are failing renewals must be reflected in the status condition. This shouldn't set
	// the overall resource phase to not ready because even though the renewal failed the already existing secret
	// would still be valid
	if len(renewalFailures) > 0 {
		msg := fmt.Sprintf("Certificate renewal is failing; the deployment is still running on the current certificate(s): %s", strings.Join(renewalFailures, "; "))
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionFalse, reasonCertificateRenewalFailed, msg)
	}

	return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "All operator-managed certificates are issued and up to date")
}

// certificatesReadyCondition builds a CertificatesReady condition. LastTransitionTime
// and ObservedGeneration are filled in by the resource's SetStatusCondition upsert.
func certificatesReadyCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    ConditionCertificatesReady,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

// issuanceFailedCondition is the CertificatesReady=False condition paired with a
// workflow.Failed while ensuring or inspecting certificates.
func issuanceFailedCondition(message string) metav1.Condition {
	return certificatesReadyCondition(metav1.ConditionFalse, reasonCertificateIssuanceError, message)
}

// certInspection captures the readiness and failure state of a single Certificate.
type certInspection struct {
	name string
	// ready is true when cert-manager has issued a usable cert for the current Certificate spec.
	ready bool
	// renewalFailed is true when the cert is ready but a renewal attempt is failing. The
	// deployment keeps running on the current cert, so this is only a soft warning.
	renewalFailed bool
	// issuanceFailing is true when an issuance attempt is failing for currently created Certificate.
	// Together with !ready it means there is no usable cert yet (surfaced in the Pending message).
	issuanceFailing bool
	// detail is a human readable line describing the failure (attempt count and last time).
	detail string
}

// inspectCertificate reads one Certificate's status, whether it holds a usable (Ready) cert,
// and whether an issuance/renewal attempt is failing.
func inspectCertificate(ctx context.Context, c kubernetesClient.Client, namespace, name string) (certInspection, workflow.Status) {
	cert := &certmanagerv1.Certificate{}
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cert); err != nil {
		if meta.IsNoMatchError(err) {
			return certInspection{}, workflow.Failed(xerrors.Errorf(
				"cert-manager is required for operator-managed certificates but its CRDs are not installed in the cluster"))
		}
		if apiErrors.IsNotFound(err) {
			return certInspection{name: name, ready: false}, workflow.OK()
		}
		return certInspection{}, workflow.Failed(err)
	}
	return inspectCertificateStatus(name, cert), workflow.OK()
}

// inspectCertificateStatus classifies a fetched Certificate's readiness and failure state.
func inspectCertificateStatus(name string, cert *certmanagerv1.Certificate) certInspection {
	insp := certInspection{name: name, ready: certificateReady(cert)}
	// cert-manager sets LastFailureTime when an issuance attempt fails and clears it on the
	// next success, so a non-nil value means the most recent attempt failed and has not yet
	// been cleared. Surface it whether or not the cert is currently ready.
	if cert.Status.LastFailureTime != nil {
		attempts := 0
		if cert.Status.FailedIssuanceAttempts != nil {
			attempts = *cert.Status.FailedIssuanceAttempts
		}
		insp.detail = fmt.Sprintf("certificate %s is failing renewal, has %d failed attempt(s), last at %s)", name, attempts, cert.Status.LastFailureTime.Time.UTC().Format(time.RFC3339))
		if insp.ready {
			// The current cert is still valid; a renewal is failing in the background.
			insp.renewalFailed = true
		} else {
			// No usable cert yet (insp.ready is false) and issuance is failing (a first issuance, or a spec change
			// whose new issuance is failing).
			insp.issuanceFailing = true
		}
	}
	return insp
}

// componentCertificates returns the per-component Certificates the operator must issue
// for one component's Options (an RS/standalone has a single component; a sharded cluster
// has one per mongos/config/shard)
func componentCertificates(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, opts Options) []certToEnsure {
	sec := res.GetSecurity()

	// SAN count for the shared member (server) cert, max(current, desired), but never below what
	// the cert already covers (the annotation value in certificate resource). Holding the count
	// steady across a scale avoids reissuing the cert at every one-at-a-time step, which would
	// churn its hash-named file and can leave agents loading a file that's already been deleted.
	// Names are rebuilt from current config each reconcile; only the count is held.
	covered := readCoveredMembersCount(ctx, c, res, opts.CertSecretName)
	toCover := resolveMembersToCover(opts, covered)
	// we create memberDNSOpts and set Replicas to toCover so that the buildDNSNames generates
	// the DNS names for the resolved member count rather than opts.Replicas.
	memberDNSOpts := opts
	memberDNSOpts.Replicas = toCover
	dnsNames := buildDNSNames(memberDNSOpts)

	memberCertAnnotations := map[string]string{coveredMembersAnnotation: strconv.Itoa(toCover)}
	internalX509 := sec.GetInternalClusterAuthenticationMode() == util.X509

	var memberSub *certmanagerv1.X509Subject
	var memberCN string
	// the membership subject when X509 internal auth is on (mongod matches peers by subject),
	// else the optional subjects.server override. When that override is unset the subject has
	// no DN but only the SANs.
	if internalX509 {
		memberSub, memberCN = membershipSubject(res)
	} else {
		memberSub, memberCN = serverSubject(res)
	}

	required := []certToEnsure{
		// member/server cert (serverAuth EKU), signed by the server-category issuer.
		{
			certName:    opts.CertSecretName,
			spec:        certSpec(res, categoryServer, opts.CertSecretName, dnsNames, memberCN, memberSub, memberServerCertUsages),
			annotations: memberCertAnnotations,
		},
	}

	// internal-cluster clusterFile (clientAuth), signed by the client-category issuer.
	// No dnsNames here, this is a client cert that mongod matches by subject (O/OU/DC),
	// not by SAN. Adding per-member SANs would make it change on every scale and force a
	// needless reissue (the agent cert, also clientAuth, is issued with no SANs for the
	// same reason).
	if internalX509 && opts.InternalClusterSecretName != "" {
		sub, cn := membershipSubject(res)
		// cert-manager rejects a Certificate that has no name at all, and the membership
		// subject sets only O/OU (no common name). Since we also set no SANs, give the
		// clusterFile a stable common name so cert-manager accepts it.
		if cn == "" {
			cn = res.GetName() + "-clusterfile"
		}
		required = append(required, certToEnsure{
			certName: opts.InternalClusterSecretName,
			spec:     certSpec(res, categoryClient, opts.InternalClusterSecretName, nil, cn, sub, clientAuthCertUsages),
		})
	}

	return required
}

// agentCertificate returns the per-deployment agent client Certificate, issued when
// the agent auth mechanism is X509.
func agentCertificate(res CertificateOwner) (certToEnsure, bool) {
	sec := res.GetSecurity()
	if sec.GetAgentMechanism("") != util.X509 {
		return certToEnsure{}, false
	}
	name := sec.AgentClientCertificateSecretName(res.GetName())
	sub, cn := agentSubject(res)
	return certToEnsure{
		certName: name,
		spec:     certSpec(res, categoryClient, name, nil, cn, sub, clientAuthCertUsages),
	}, true
}

// certSpec builds a CertificateSpec. The signer is resolved from the certificate's
// category (user issuer or the operator's self-signed CA issuer), and duration/renewBefore
// come from that category's config; dnsNames, commonName, subject and usages are per-cert.
func certSpec(
	res CertificateOwner,
	cat certCategory,
	secretName string,
	dnsNames []string,
	commonName string,
	subject *certmanagerv1.X509Subject,
	usages []certmanagerv1.KeyUsage,
) certmanagerv1.CertificateSpec {
	spec := certmanagerv1.CertificateSpec{
		SecretName: secretName,
		DNSNames:   dnsNames,
		CommonName: commonName,
		Subject:    subject,
		Usages:     usages,
		IssuerRef:  signerIssuerRef(res, cat),
	}
	if cfg := certCategoryConfig(res, cat); cfg != nil {
		spec.Duration = cfg.Duration
		spec.RenewBefore = cfg.RenewBefore
	}
	return spec
}

// userConfiguredSubjects returns the user-configured subject overrides (nil-safe).
func userConfiguredSubjects(res CertificateOwner) *mdbv1.CertSubjects {
	if cm := res.GetSecurity().ManagedCertificate; cm != nil {
		return cm.Subjects
	}
	return nil
}

// membershipSubject is the X509 membership identity shared by the member and clusterFile certs.
func membershipSubject(res CertificateOwner) (*certmanagerv1.X509Subject, string) {
	def := &certmanagerv1.X509Subject{
		Organizations:       []string{res.GetName() + "-server"},
		OrganizationalUnits: []string{res.GetNamespace()},
	}
	var override *mdbv1.X509Subject
	if s := userConfiguredSubjects(res); s != nil {
		override = s.Membership
	}
	return mergeSubject(override, def, "")
}

// agentSubject is the agent's X509 user identity. By default it differs from the membership
// subject in O (O=<name>-agent vs O=<name>-server) so mongod does not classify the agent as a
// cluster peer. verifyAgentSubjectDistinct enforces that this still holds when the user
// overrides subjects.agent or subjects.membership.
func agentSubject(res CertificateOwner) (*certmanagerv1.X509Subject, string) {
	def := &certmanagerv1.X509Subject{
		Organizations:       []string{res.GetName() + "-agent"},
		OrganizationalUnits: []string{res.GetNamespace()},
		Countries:           []string{"US"},
	}
	var override *mdbv1.X509Subject
	if s := userConfiguredSubjects(res); s != nil {
		override = s.Agent
	}
	return mergeSubject(override, def, res.GetName()+"-agent")
}

// serverSubject is the optional subject for server certs that carry no identity (the
// member cert when internal auth is NOT x509). Default is SAN-only (nil subject); a user
// override (subjects.server) supplies a DN to satisfy a server issuer's policy.
func serverSubject(res CertificateOwner) (*certmanagerv1.X509Subject, string) {
	var override *mdbv1.X509Subject
	if s := userConfiguredSubjects(res); s != nil {
		override = s.Server
	}
	if override == nil {
		return nil, ""
	}
	return mergeSubject(override, &certmanagerv1.X509Subject{}, "")
}

// mergeSubject applies a user override onto the operator default per field. We merge the user
// configured subject with operator defaults to handle the fact that customer might conifgure
// just some attributes of subject and not all (or mandatory ones).
func mergeSubject(override *mdbv1.X509Subject, def *certmanagerv1.X509Subject, defCN string) (*certmanagerv1.X509Subject, string) {
	if override == nil {
		return def, defCN
	}
	out := &certmanagerv1.X509Subject{}
	if def != nil {
		*out = *def
	}
	if len(override.Organizations) > 0 {
		out.Organizations = override.Organizations
	}
	if len(override.OrganizationalUnits) > 0 {
		out.OrganizationalUnits = override.OrganizationalUnits
	}
	if len(override.Countries) > 0 {
		out.Countries = override.Countries
	}
	if len(override.Localities) > 0 {
		out.Localities = override.Localities
	}
	if len(override.Provinces) > 0 {
		out.Provinces = override.Provinces
	}
	cn := defCN
	if override.CommonName != "" {
		cn = override.CommonName
	}
	return out, cn
}

// ensureCertificate creates or updates a single operator-owned cert-manager
// Certificate. It does not check readiness of the created certificate.
func ensureCertificate(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cert certToEnsure) workflow.Status {
	existing := &certmanagerv1.Certificate{}
	err := c.Get(ctx, types.NamespacedName{Name: cert.certName, Namespace: res.GetNamespace()}, existing)
	switch {
	case err == nil:
		existing.Spec = cert.spec
		existing.OwnerReferences = kube.BaseOwnerReference(res)
		for k, v := range cert.annotations {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[k] = v
		}
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return workflow.Failed(updateErr)
		}
	case meta.IsNoMatchError(err):
		return workflow.Failed(xerrors.Errorf(
			"cert-manager is required for operator-managed certificates but its CRDs are not installed in the cluster"))
	case apiErrors.IsNotFound(err):
		desired := &certmanagerv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Name:            cert.certName,
				Namespace:       res.GetNamespace(),
				OwnerReferences: kube.BaseOwnerReference(res),
				Annotations:     cert.annotations,
			},
			Spec: cert.spec,
		}
		if createErr := c.Create(ctx, desired); createErr != nil {
			return workflow.Failed(createErr)
		}
	default:
		return workflow.Failed(err)
	}
	return workflow.OK()
}

// resolveMembersToCover returns how many members the member cert's SANs must cover, what the
// resource wants in this reconcile (MembersToCover), but never below what the cert already covers
// (never-shrink).
func resolveMembersToCover(opts Options, coveredInCert int) int {
	n := opts.Replicas
	if opts.MembersToCover > 0 {
		n = opts.MembersToCover
	}
	if coveredInCert > n {
		n = coveredInCert
	}
	return n
}

// readCoveredMembersCount returns how many members' SANs the current member (server) certificate
// is created with, read from an annotation the operator adds on it. Returns 0 when the certificate
// or the annotation does not exist yet (e.g. the first reconcile).
func readCoveredMembersCount(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, memberCertName string) int {
	existing := &certmanagerv1.Certificate{}
	if err := c.Get(ctx, types.NamespacedName{Name: memberCertName, Namespace: res.GetNamespace()}, existing); err != nil {
		return 0
	}
	if v, ok := existing.Annotations[coveredMembersAnnotation]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// ensureSelfSignedCA idempotently creates the per-category self-signed CA chain used to
// sign a category's certs when user has not configured issuer for it. A SelfSigned Issuer
// bootstraps a self-signed CA Certificate, and a CA Issuer signs the leaves from it.
// All three objects are owned by the resource so they GC with it.
func ensureSelfSignedCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory) workflow.Status {
	if status := ensureIssuer(ctx, c, res, selfSignedIssuerName(res, cat), certmanagerv1.IssuerSpec{
		IssuerConfig: certmanagerv1.IssuerConfig{
			SelfSigned: &certmanagerv1.SelfSignedIssuer{},
		},
	}); !status.IsOK() {
		return status
	}

	// Self-signed CA certificate (isCA).
	caCert := certToEnsure{
		certName: managedCACertName(res, cat),
		spec: certmanagerv1.CertificateSpec{
			IsCA:       true,
			SecretName: managedCACertName(res, cat),
			CommonName: fmt.Sprintf("%s-%s-ca", res.GetName(), cat),
			IssuerRef:  cmmeta.ObjectReference{Name: selfSignedIssuerName(res, cat), Kind: certmanagerv1.IssuerKind},
		},
	}
	if status := ensureCertificate(ctx, c, res, caCert); !status.IsOK() {
		return status
	}

	return ensureIssuer(ctx, c, res, managedCAIssuerName(res, cat), certmanagerv1.IssuerSpec{
		IssuerConfig: certmanagerv1.IssuerConfig{
			CA: &certmanagerv1.CAIssuer{
				SecretName: managedCACertName(res, cat),
			},
		},
	})
}

// ensureIssuer creates or updates an operator-owned cert-manager Issuer.
func ensureIssuer(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, name string, spec certmanagerv1.IssuerSpec) workflow.Status {
	existing := &certmanagerv1.Issuer{}
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: res.GetNamespace()}, existing)
	switch {
	case err == nil:
		existing.Spec = spec
		existing.OwnerReferences = kube.BaseOwnerReference(res)
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return workflow.Failed(updateErr)
		}
	case meta.IsNoMatchError(err):
		return workflow.Failed(xerrors.Errorf(
			"cert-manager is required for operator-managed certificates but its CRDs are not installed in the cluster"))
	case apiErrors.IsNotFound(err):
		desired := &certmanagerv1.Issuer{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       res.GetNamespace(),
				OwnerReferences: kube.BaseOwnerReference(res),
			},
			Spec: spec,
		}
		if createErr := c.Create(ctx, desired); createErr != nil {
			return workflow.Failed(createErr)
		}
	default:
		return workflow.Failed(err)
	}
	return workflow.OK()
}

// EnsureCAConfigMapFromIssuedSecret writes the operator-owned CA ConfigMaps that are later mounted to
// workload pods. Each per-category trust bundle lives in its own ConfigMap wired to two distinct
// mongod config fields, net.tls.CAFile (server CA) and net.tls.clsuterCAFile (client CA).
// Each category's CA is the user issuer's CA (issued secret's ca.crt, or the category's `ca` input
// (server/client.ca) when the issuer emits none) OR that category's operator self-signed CA cert,
// plus any `additionalTrustedCAs` for that category.
func EnsureCAConfigMapFromIssuedSecret(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	opts Options,
	hasClientCerts bool,
	log *zap.SugaredLogger,
) workflow.Status {
	serverCA, status := resolveCategoryCA(ctx, c, res, categoryServer, opts.CertSecretName)
	if !status.IsOK() {
		return status
	}
	// concatenate additionalTrustedCAs
	serverBundle, status := categoryBundle(ctx, c, res, categoryServer, serverCA)
	if !status.IsOK() {
		return status
	}
	serverCM := configmap.Builder().
		SetName(ManagedServerCABundleConfigMapName(res.GetName())).
		SetNamespace(res.GetNamespace()).
		SetOwnerReferences(kube.BaseOwnerReference(res)).
		SetDataField(tls.CAConfigMapKey, serverBundle).
		Build()
	if err := configmap.CreateOrUpdate(ctx, c, serverCM); err != nil {
		return workflow.Failed(err)
	}

	if hasClientCerts {
		clientCA, status := resolveCategoryCA(ctx, c, res, categoryClient, clientIssuedSecretName(res, opts))
		if !status.IsOK() {
			return status
		}
		// concatenate additionalTrustedCAs
		clientBundle, status := categoryBundle(ctx, c, res, categoryClient, clientCA)
		if !status.IsOK() {
			return status
		}
		clientCM := configmap.Builder().
			SetName(ManagedClientCABundleConfigMapName(res.GetName())).
			SetNamespace(res.GetNamespace()).
			SetOwnerReferences(kube.BaseOwnerReference(res)).
			SetDataField(tls.ClusterCAConfigMapKey, clientBundle).
			Build()
		if err := configmap.CreateOrUpdate(ctx, c, clientCM); err != nil {
			return workflow.Failed(err)
		}
	}

	log.Debugf("Populated operator-managed CA ConfigMaps for %s (server always; client: %t)", res.GetName(), hasClientCerts)
	return workflow.OK()
}

// resolveCategoryCA returns the primary CA cert for a category's trust bundle: the
// category's operator self-signed CA cert when it is self-signed, else the user issuer's
// CA (from the issued leaf secret's ca.crt, falling back to the category's `ca` input).
func resolveCategoryCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	issuedSecretName string,
) (string, workflow.Status) {
	if !hasUserIssuer(res, cat) {
		return readSelfSignedCACert(ctx, c, res, cat)
	}
	return resolveUserCA(ctx, c, res, cat, issuedSecretName)
}

// categoryBundle concatenates a category's primary CA with any additionalTrustedCAs.
// Every PEM block is newline-terminated so the concatenation stays a valid bundle.
func categoryBundle(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, primaryCA string) (string, workflow.Status) {
	bundle := strings.TrimRight(primaryCA, "\n") + "\n"
	cfg := certCategoryConfig(res, cat)
	if cfg == nil {
		return bundle, workflow.OK()
	}
	for _, src := range cfg.AdditionalTrustedCAs {
		ca, status := readCASource(ctx, c, res, src)
		if !status.IsOK() {
			return "", status
		}
		bundle += strings.TrimRight(ca, "\n") + "\n"
	}
	return bundle, workflow.OK()
}

// resolveUserCA returns the CA that verifies a category's certs when signed by a user
// issuer. We figure it out from the issued cert secret's ca.crt (emitted by CA-type issuers).
// Or fallback to the category's `ca` input, for issuers that do not emit ca.crt in certificate secret.
func resolveUserCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, issuedSecretName string) (string, workflow.Status) {
	issued, err := c.GetSecret(ctx, types.NamespacedName{Name: issuedSecretName, Namespace: res.GetNamespace()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return "", workflow.Pending("Waiting for cert-manager to write the certificate secret %s", issuedSecretName)
		}
		return "", workflow.Failed(err)
	}
	if caBytes, ok := issued.Data[corev1.ServiceAccountRootCAKey]; ok && len(caBytes) > 0 {
		return string(caBytes), workflow.OK()
	}
	// The issuer didn't emit its CA to ca.crt, fall back to the category's `ca` input.
	if cfg := certCategoryConfig(res, cat); cfg != nil && cfg.CA != nil {
		return readCASource(ctx, c, res, *cfg.CA)
	}
	return "", workflow.Failed(xerrors.Errorf(
		"the %s issuer did not populate ca.crt in the certificate secret and no CA was provided via security.managedCertificate.%s.ca; set it to a ConfigMap/Secret holding the issuer's CA (required for issuers that do not emit ca.crt)",
		cat, cat))
}

// readCASource reads a CA certificate from a CARef reference (a ConfigMap or a
// Secret, under the given key).
func readCASource(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, src mdbv1.CARef) (string, workflow.Status) {
	nn := types.NamespacedName{Name: src.Name, Namespace: res.GetNamespace()}
	if src.Kind == "Secret" {
		s, err := c.GetSecret(ctx, nn)
		if err != nil {
			if apiErrors.IsNotFound(err) {
				return "", workflow.Failed(xerrors.Errorf("CA Secret %q not found in namespace %s", src.Name, res.GetNamespace()))
			}
			return "", workflow.Failed(err)
		}
		if v := s.Data[src.Key]; len(v) > 0 {
			return string(v), workflow.OK()
		}
		return "", workflow.Failed(xerrors.Errorf("CA Secret %q has no data under key %q", src.Name, src.Key))
	}
	cm, err := c.GetConfigMap(ctx, nn)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return "", workflow.Failed(xerrors.Errorf("CA ConfigMap %q not found in namespace %s", src.Name, res.GetNamespace()))
		}
		return "", workflow.Failed(err)
	}
	if v := cm.Data[src.Key]; v != "" {
		return v, workflow.OK()
	}
	return "", workflow.Failed(xerrors.Errorf("CA ConfigMap %q has no data under key %q", src.Name, src.Key))
}

// clientIssuedSecretName returns the name of a client-category issued leaf secret to read
// the client issuer's ca.crt from (the clusterFile secret, else the agent secret). Only
// meaningful when the resource has client certs.
func clientIssuedSecretName(res CertificateOwner, opts Options) string {
	sec := res.GetSecurity()
	if sec.GetInternalClusterAuthenticationMode() == util.X509 && opts.InternalClusterSecretName != "" {
		return opts.InternalClusterSecretName
	}
	if sec.GetAgentMechanism("") == util.X509 {
		return sec.AgentClientCertificateSecretName(res.GetName())
	}
	return ""
}

// readSelfSignedCACert reads a category's operator-managed self-signed CA certificate
// (the CA cert only, never its private key) from the secret cert-manager wrote for the CA
// Certificate.
func readSelfSignedCACert(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory) (string, workflow.Status) {
	name := managedCACertName(res, cat)
	caSecret, err := c.GetSecret(ctx, types.NamespacedName{Name: name, Namespace: res.GetNamespace()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return "", workflow.Pending("Waiting for cert-manager to write the self-signed CA secret %s", name)
		}
		return "", workflow.Failed(err)
	}
	ca, ok := caSecret.Data[corev1.ServiceAccountRootCAKey]
	if !ok || len(ca) == 0 {
		return "", workflow.Failed(xerrors.Errorf("self-signed CA secret %s has no ca.crt", name))
	}
	return string(ca), workflow.OK()
}

// ManagedServerCABundleConfigMapName is the name of the operator-owned server-category CA
// trust-bundle ConfigMap in managed-certificate mode ("<name>-managed-server-ca-bundle").
func ManagedServerCABundleConfigMapName(resourceName string) string {
	return resourceName + "-managed-server-ca-bundle"
}

// ManagedClientCABundleConfigMapName is the operator-owned client-category CA trust-bundle
// ConfigMap in managed-certificate mode ("<name>-managed-client-ca-bundle").
func ManagedClientCABundleConfigMapName(resourceName string) string {
	return resourceName + "-managed-client-ca-bundle"
}

func buildDNSNames(opts Options) []string {
	hostnames, _ := GetDNSNames(opts)
	dnsNames := append([]string{}, hostnames...)
	for i := 0; i < opts.Replicas; i++ {
		dnsNames = append(dnsNames, GetAdditionalCertDomainsForMember(opts, i)...)
	}
	// External access exposes each member as <pod>.<externalDomain>. List those real member
	// hostnames instead of a "*." wildcard so the cert only asserts the actual names. The pod
	// hostnames are ordinal-deterministic, so a scale-up within the covered count reuses names
	// already in the cert and does not reissue.
	dnsNames = append(dnsNames, GetExternalDNSNames(opts)...)
	return dnsNames
}

// verifyAgentSubjectDistinct rejects a config where the agent cert's subject would match the
// membership subject on both O and OU. mongod recognizes a cluster peer by comparing an
// incoming cert's O/OU attributes against the node's member cert; if the
// agent cert shares both, mongod treats the automation agent as a peer instead of an
// $external user and agent auth breaks.
func verifyAgentSubjectDistinct(res CertificateOwner) workflow.Status {
	sec := res.GetSecurity()
	if sec.GetInternalClusterAuthenticationMode() != util.X509 || sec.GetAgentMechanism("") != util.X509 {
		return workflow.OK()
	}
	memberSub, _ := membershipSubject(res)
	agentSub, _ := agentSubject(res)
	if slices.Equal(memberSub.Organizations, agentSub.Organizations) &&
		slices.Equal(memberSub.OrganizationalUnits, agentSub.OrganizationalUnits) {
		return workflow.Failed(xerrors.Errorf(
			"the agent subject must differ from the membership subject in Organization (O) or "+
				"OrganizationalUnit (OU): both resolve to O=%v, OU=%v. Set "+
				"security.managedCertificate.subjects.agent to a distinct O or OU.",
			agentSub.Organizations, agentSub.OrganizationalUnits))
	}
	return workflow.OK()
}

// verifyMembershipSubjects enforces the X509-internal-auth invariant that, per component,
// the member cert (#1, server-signed) and the internal-cluster clusterFile cert (#2,
// client-signed) carry the SAME membership subject (O/OU). mongod recognizes a peer by
// comparing the presented cert's O/OU against the accepting node's member cert, so a
// divergence (e.g. a user issuer that rewrites/enforces the subject on one side) makes
// peers unrecognizable and the replica set never forms. Reading the ISSUED certs and
// failing loud turns that into an actionable error. No-op unless internal auth is X509.
func verifyMembershipSubjects(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, allOpts []Options) workflow.Status {
	if res.GetSecurity().GetInternalClusterAuthenticationMode() != util.X509 {
		return workflow.OK()
	}
	for _, opts := range allOpts {
		if opts.InternalClusterSecretName == "" {
			continue
		}
		memberO, memberOU, status := readCertOrgUnits(ctx, c, res, opts.CertSecretName)
		if !status.IsOK() {
			return status
		}
		clusterO, clusterOU, status := readCertOrgUnits(ctx, c, res, opts.InternalClusterSecretName)
		if !status.IsOK() {
			return status
		}
		if !slices.Equal(memberO, clusterO) || !slices.Equal(memberOU, clusterOU) {
			return workflow.Failed(xerrors.Errorf(
				"membership subject mismatch: the member certificate %q (O=%v, OU=%v) and the internal-cluster clusterFile certificate %q (O=%v, OU=%v) must share the same subject for mongod to recognize cluster peers under x509 internal auth. This typically means the server and client signers produced different subjects — align their subject policy, use the same issuer for both categories, or self-sign the affected category",
				opts.CertSecretName, memberO, memberOU, opts.InternalClusterSecretName, clusterO, clusterOU))
		}
	}
	return workflow.OK()
}

// readCertOrgUnits parses the issued leaf certificate (tls.crt) from a secret and returns
// its subject Organization and OrganizationalUnit — the attributes mongod compares for
// cluster membership.
func readCertOrgUnits(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, secretName string) ([]string, []string, workflow.Status) {
	s, err := c.GetSecret(ctx, types.NamespacedName{Name: secretName, Namespace: res.GetNamespace()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, nil, workflow.Pending("Waiting for cert-manager to write the certificate secret %s", secretName)
		}
		return nil, nil, workflow.Failed(err)
	}
	block, _ := pem.Decode(s.Data[corev1.TLSCertKey])
	if block == nil {
		return nil, nil, workflow.Failed(xerrors.Errorf("could not PEM-decode tls.crt in secret %s", secretName))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, workflow.Failed(xerrors.Errorf("could not parse certificate in secret %s: %w", secretName, err))
	}
	return cert.Subject.Organization, cert.Subject.OrganizationalUnit, workflow.OK()
}

func certificateReady(cert *certmanagerv1.Certificate) bool {
	for _, cond := range cert.Status.Conditions {
		if cond.Type == certmanagerv1.CertificateConditionReady {
			return cond.Status == cmmeta.ConditionTrue &&
				cond.ObservedGeneration == cert.Generation
		}
	}
	return false
}
