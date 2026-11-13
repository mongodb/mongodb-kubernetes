package certs

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
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

// certificateOwnerReferences returns the owner references of the objects issued for res. An adapter
// can name certificates after one resource (e.g. the AppDB) while another (the Ops Manager
// resource) owns them by implementing ownerResource.
func certificateOwnerReferences(res CertificateOwner) []metav1.OwnerReference {
	if o, ok := res.(interface{ ownerResource() v1.ObjectOwner }); ok {
		return kube.BaseOwnerReference(o.ownerResource())
	}
	return kube.BaseOwnerReference(res)
}

var memberServerCertUsages = []certmanagerv1.KeyUsage{
	certmanagerv1.UsageDigitalSignature,
	certmanagerv1.UsageKeyEncipherment,
	certmanagerv1.UsageServerAuth,
}

// memberServerAndClientCertUsages is for a member cert that is also a client cert. Without x509
// internal cluster authentication there is no clusterfile, so mongod also presents the member cert
// when it connects to the other members, and they reject it unless it allows clientAuth.
var memberServerAndClientCertUsages = []certmanagerv1.KeyUsage{
	certmanagerv1.UsageDigitalSignature,
	certmanagerv1.UsageKeyEncipherment,
	certmanagerv1.UsageServerAuth,
	certmanagerv1.UsageClientAuth,
}

// WithholdsClientCertificate returns whether a mongodb CR sets
// setParameter.tlsWithholdClientCertificate to true. A process that withholds its certificate does not
// send it when it connects to the other members of the deployment: without a clusterfile that is the
// member certificate, so the certificate is never used as a client certificate. Only a boolean true counts.
func WithholdsClientCertificate(cfg *mdbv1.AdditionalMongodConfig) bool {
	if cfg == nil {
		return false
	}
	setParameter, ok := cfg.ToMap()["setParameter"].(map[string]any)
	if !ok {
		return false
	}
	withhold, ok := setParameter["tlsWithholdClientCertificate"].(bool)
	return ok && withhold
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

// coveredMembersAnnotation stores how many members' SANs the operator-managed member/server
// certificate currently covers. It is what keeps the certificate from shrinking on scale down. The
// operator recomputes the cert's SANs on every reconcile, and without this count the SAN list would
// lose one member on every reconcile during a scale down, so the cert would be reissued each time which
// we want to avoid.
const coveredMembersAnnotation = "mongodb.com/covered-members"

// coveredMembersPerClusterAnnotation stores, per member cluster, how many members' SANs are
// currently baked into the multi-cluster member certificate. Same purpose as
// coveredMembersAnnotation but for the multi-cluster case, where one certificate covers several
// clusters so the count has to be held per cluster. The value is a JSON object of
// cluster name -> member count.
const coveredMembersPerClusterAnnotation = "mongodb.com/covered-members-per-cluster"

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
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "Operator-managed certificates are not required for this resource")
	}

	// Validate the computed subjects before issuing anything, the agent subject must not match
	// the membership subject on O and OU attributes.
	if status := verifyAgentSubjectDistinct(res); !status.IsOK() {
		return status, issuanceFailedCondition(status.Message())
	}

	hasClientCerts := res.GetSecurity().RequiresX509ClientCerts()

	// figure out all the certificates that are required for the reconciling deployment
	var required []certToEnsure
	for _, opts := range allOpts {
		required = append(required, componentCertificates(ctx, c, res, opts)...)
	}
	// agent cert is per deployment not per component (options)
	if agentCert, ok := agentCertificate(res); ok {
		required = append(required, agentCert)
	}

	renewalFailures, status, condition := ensureCertificatesReady(ctx, c, res, required, hasClientCerts)
	if !status.IsOK() {
		return status, condition
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

	return certificatesResult(renewalFailures)
}

// ensureCertificatesReady ensures the self-signed CA(s) when no user issuer is configured and the
// required Certificates, then waits for all of them to be ready. It returns the certificates whose
// renewal is failing, or a non-OK status when the caller must stop and return it.
func ensureCertificatesReady(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	required []certToEnsure,
	hasClientCerts bool,
) ([]string, workflow.Status, metav1.Condition) {
	// setup the operator managed per category (client/server) self signed CA(s) if it's not
	// configured via user
	serverSelfSigned := !hasUserIssuer(res, categoryServer)
	clientSelfSigned := hasClientCerts && !hasUserIssuer(res, categoryClient)

	type Certificate struct {
		name       string
		generation int64
	}
	writtenCertificates := make([]Certificate, 0, len(required)+2)
	if serverSelfSigned {
		generation, status := ensureSelfSignedCA(ctx, c, res, categoryServer)
		if !status.IsOK() {
			return nil, status, issuanceFailedCondition(status.Message())
		}
		writtenCertificates = append(writtenCertificates, Certificate{name: managedCACertName(res, categoryServer), generation: generation})
	}
	if clientSelfSigned {
		generation, status := ensureSelfSignedCA(ctx, c, res, categoryClient)
		if !status.IsOK() {
			return nil, status, issuanceFailedCondition(status.Message())
		}
		writtenCertificates = append(writtenCertificates, Certificate{name: managedCACertName(res, categoryClient), generation: generation})
	}

	// Ensure (create/patch) all required certs first, don't wait for certs to
	// be ready immediately after creating them
	for _, cert := range required {
		generation, status := ensureCertificate(ctx, c, res, cert)
		if !status.IsOK() {
			return nil, status, issuanceFailedCondition(status.Message())
		}
		writtenCertificates = append(writtenCertificates, Certificate{name: cert.certName, generation: generation})
	}

	// check the status of resources and categorise them. Ones that are not ready yet, ones
	// that are not ready and whose issuance is actively failing (so we can surface the error),
	// and ready ones whose renewal is failing (a soft warning).
	var notReady, issuanceFailures, renewalFailures []string
	for _, cert := range writtenCertificates {
		insp, status := inspectCertificateAtGeneration(ctx, c, res.GetNamespace(), cert.name, cert.generation)
		if !status.IsOK() {
			return nil, status, issuanceFailedCondition(status.Message())
		}
		switch {
		case !insp.ready:
			notReady = append(notReady, cert.name)
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
		return nil, workflow.Pending("%s", msg), certificatesReadyCondition(metav1.ConditionFalse, reasonCertificatesIssuing, msg)
	}

	// A Ready condition and the issued Secret are written independently. Require every
	// leaf's output Secret regardless of whether its CA is self-signed or user provided,
	// so a deleted/not-yet-written leaf cannot be reported as current.
	if status, condition := requireIssuedSecrets(ctx, c, res, required); !status.IsOK() {
		return nil, status, condition
	}

	// every issued certificate must have the key usages it was requested with
	if status := verifyKeyUsagesInIssuedCert(ctx, c, res, required); !status.IsOK() {
		return nil, status, issuanceFailedCondition(status.Message())
	}

	return renewalFailures, workflow.OK(), metav1.Condition{}
}

// certificatesResult is the final CertificatesReady condition once all certificates are ready.
func certificatesResult(renewalFailures []string) (workflow.Status, metav1.Condition) {
	// the certificates that are failing renewals must be reflected in the status condition. This shouldn't set
	// the overall resource phase to not ready because even though the renewal failed the already existing secret
	// would still be valid
	if len(renewalFailures) > 0 {
		msg := fmt.Sprintf("Certificate renewal is failing; the deployment is still running on the current certificate(s): %s", strings.Join(renewalFailures, "; "))
		return workflow.OK(), certificatesReadyCondition(metav1.ConditionFalse, reasonCertificateRenewalFailed, msg)
	}

	return workflow.OK(), certificatesReadyCondition(metav1.ConditionTrue, reasonAllCertificatesReady, "All operator-managed certificates are issued and up to date")
}

// requireIssuedSecrets checks that cert-manager wrote the output Secret of every issued leaf
// Certificate. Ready status and Secret visibility are independent writes, and Secret deletion
// can precede the readiness update. This check is CA-source agnostic: self-signed certificates
// need the leaf Secret just as much as user-issuer certificates do.
func requireIssuedSecrets(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, required []certToEnsure) (workflow.Status, metav1.Condition) {
	var missing []string
	for _, cert := range required {
		secretName := cert.spec.SecretName
		if secretName == "" {
			secretName = cert.certName
		}
		if _, err := c.GetSecret(ctx, types.NamespacedName{Name: secretName, Namespace: res.GetNamespace()}); err != nil {
			if apiErrors.IsNotFound(err) {
				missing = append(missing, secretName)
				continue
			}
			return workflow.Failed(err), issuanceFailedCondition(err.Error())
		}
	}
	if len(missing) == 0 {
		return workflow.OK(), metav1.Condition{}
	}
	msg := waitingForIssuedSecretsMessage(missing)
	return workflow.Pending("%s", msg), certificatesReadyCondition(metav1.ConditionFalse, reasonCertificatesIssuing, msg)
}

func waitingForIssuedSecretsMessage(secretNames []string) string {
	if len(secretNames) == 1 {
		return fmt.Sprintf("Waiting for cert-manager to write the certificate secret %s", secretNames[0])
	}
	return fmt.Sprintf("Waiting for cert-manager to write the certificate secrets %s", strings.Join(secretNames, ", "))
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
	return inspectCertificateAtGeneration(ctx, c, namespace, name, 0)
}

func inspectCertificateAtGeneration(ctx context.Context, c kubernetesClient.Client, namespace, name string, writtenGeneration int64) (certInspection, workflow.Status) {
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
	return inspectCertificateStatusAtGeneration(name, cert, writtenGeneration), workflow.OK()
}

func inspectCertificateStatusAtGeneration(name string, cert *certmanagerv1.Certificate, writtenGeneration int64) certInspection {
	insp := certInspection{name: name, ready: certificateReadyAtGeneration(cert, writtenGeneration)}
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
// has one per mongos/config/shard).
// In returns two certToEnsure types, representing member certificate and clusterFile
// certificate when x509 internal cluster auth is on.
func componentCertificates(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, opts Options) []certToEnsure {
	sec := res.GetSecurity()

	// SANs for the shared member (server) cert. For a multi-cluster replica set one cert covers
	// every member cluster, so its SANs are the union across clusters and the never-shrink count is
	// held per cluster. For everything else it is a single count: max(current, desired), but never
	// below what the cert already covers (the annotation on the certificate). Holding the count
	// steady across a scale avoids reissuing the cert at every one-at-a-time step, which would
	// churn its hash-named file and can leave agents loading a file that's already been deleted.
	// Names are rebuilt from current config each reconcile; only the count is held.
	var dnsNames []string
	var memberCertAnnotations map[string]string
	if len(opts.MultiClusterMembers) > 0 {
		// multi cluster deployment
		dnsNames, memberCertAnnotations = multiClusterMemberSANs(ctx, c, res, opts)
	} else {
		covered := readCoveredMembersCount(ctx, c, res, opts.CertSecretName)
		toCover := resolveMembersToCover(opts, covered)
		// we create memberDNSOpts and set Replicas to toCover so that the buildDNSNames generates
		// the DNS names for the resolved member count rather than opts.Replicas.
		memberDNSOpts := opts
		memberDNSOpts.Replicas = toCover
		dnsNames = buildDNSNames(memberDNSOpts)
		memberCertAnnotations = map[string]string{coveredMembersAnnotation: strconv.Itoa(toCover)}
	}
	internalX509 := sec.GetInternalClusterAuthenticationMode() == util.X509

	// The subject depends on the internal cluster authentication. With x509 mongod matches a peer by
	// the subject of its certificate, so the member certificate carries the membership subject.
	// Otherwise nothing reads the subject, so it is set only when the user configured subjects.server.
	var memberSub *certmanagerv1.X509Subject
	var memberCN string
	memberUsages := memberServerCertUsages
	if internalX509 {
		memberSub, memberCN = membershipSubject(res)
	} else {
		memberSub, memberCN = serverSubject(res)
		// Member to member connections are mutual TLS even without x509 internal authentication, and
		// there is no clusterfile, so the member certificate is what they present to each other even as client,
		// that's why it needs both key usages. A deployment whose mongod config withholds the
		// certificate presents nothing to the other members, so the client usage is not needed.
		if !opts.WithholdsClientCertificate {
			memberUsages = memberServerAndClientCertUsages
		}
	}

	required := []certToEnsure{
		// member/server cert, signed by the server-category issuer.
		{
			certName:    opts.CertSecretName,
			spec:        certSpec(res, categoryServer, opts.CertSecretName, dnsNames, memberCN, memberSub, memberUsages),
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

// ensureCertificate creates or updates an operator-owned Certificate and returns its written generation.
func ensureCertificate(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cert certToEnsure) (int64, workflow.Status) {
	existing := &certmanagerv1.Certificate{}
	err := c.Get(ctx, types.NamespacedName{Name: cert.certName, Namespace: res.GetNamespace()}, existing)
	switch {
	case err == nil:
		existing.Spec = cert.spec
		existing.OwnerReferences = certificateOwnerReferences(res)
		for k, v := range cert.annotations {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[k] = v
		}
		if updateErr := c.Update(ctx, existing); updateErr != nil {
			return 0, workflow.Failed(updateErr)
		}
		return existing.Generation, workflow.OK()
	case meta.IsNoMatchError(err):
		return 0, workflow.Failed(xerrors.Errorf(
			"cert-manager is required for operator-managed certificates but its CRDs are not installed in the cluster"))
	case apiErrors.IsNotFound(err):
		desired := &certmanagerv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Name:            cert.certName,
				Namespace:       res.GetNamespace(),
				OwnerReferences: certificateOwnerReferences(res),
				Annotations:     cert.annotations,
			},
			Spec: cert.spec,
		}
		if createErr := c.Create(ctx, desired); createErr != nil {
			return 0, workflow.Failed(createErr)
		}
		return desired.Generation, workflow.OK()
	default:
		return 0, workflow.Failed(err)
	}
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

// multiClusterMemberSANs builds the union SAN list for a multi-cluster member certificate and the
// per-cluster never-shrink annotation. Each cluster covers max(desired, previously covered) members,
// so a scale-up jumps to the target once and a scale-down holds the SANs steady.
func multiClusterMemberSANs(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, opts Options) ([]string, map[string]string) {
	covered := coveredMembersPerCluster(ctx, c, res, opts.CertSecretName)
	resolved, perClusterCount := resolveMultiClusterMembersToCover(opts.MultiClusterMembers, covered)

	var dnsNames []string
	for _, m := range resolved {
		dnsNames = append(dnsNames, memberClusterDNSNames(opts, m.ClusterNum, m.Members, m.ExternalDomain)...)
	}

	encoded, err := json.Marshal(perClusterCount)
	if err != nil {
		// fall back to an empty object rather than fail.
		encoded = []byte("{}")
	}
	return dnsNames, map[string]string{coveredMembersPerClusterAnnotation: string(encoded)}
}

// resolveMultiClusterMembersToCover decides how many members' SANs the member certificate should cover in
// each member cluster, the bigger of what the cluster wants now and what the certificate already covers.
// So a scale-up grows the certificate, but a scale-down never shrinks it. It returns the counts as a list,
// used to build the SANs, and as a map, saved in the certificate annotation for the next reconcile.
func resolveMultiClusterMembersToCover(clusters []MemberCluster, covered map[string]int) ([]MemberCluster, map[string]int) {
	resolved := make([]MemberCluster, 0, len(clusters))
	perClusterCount := make(map[string]int, len(clusters))
	for _, m := range clusters {
		members := m.Members
		if prev := covered[m.ClusterName]; prev > members {
			members = prev
		}
		perClusterCount[m.ClusterName] = members
		resolved = append(resolved, MemberCluster{
			ClusterNum:     m.ClusterNum,
			ClusterName:    m.ClusterName,
			Members:        members,
			ExternalDomain: m.ExternalDomain,
		})
	}
	return resolved, perClusterCount
}

// memberClusterDNSNames returns the SANs for one member cluster, the internal per-pod service
// FQDNs for its members, plus the external hostnames when that cluster is externally exposed. Both
// are listed so the certificate works for internal and external connections.
func memberClusterDNSNames(opts Options, clusterNum, members int, externalDomain *string) []string {
	names, podNames := dns.GetMultiClusterProcessHostnamesAndPodNames(opts.ResourceName, opts.Namespace, clusterNum, members, opts.ClusterDomain, nil)
	if externalDomain != nil {
		names = append(names, dns.GetMultiClusterProcessHostnames(opts.ResourceName, opts.Namespace, clusterNum, members, opts.ClusterDomain, externalDomain)...)
	}
	// additionalCertificateDomains is <pod>.<domain> for every member, the same names the single
	// cluster path adds. The sharded check asks for them, so a sharded component's certificate has
	// to carry them too. A multi-cluster replica set leaves the field empty and is unaffected.
	for _, podName := range podNames {
		for _, domain := range opts.additionalCertificateDomains {
			names = append(names, podName+"."+domain)
		}
	}
	return names
}

// coveredMembersPerCluster returns, for each member cluster, how many members' SANs the member
// certificate covers. The operator stores these counts in an annotation on the certificate. Returns an
// empty map when the certificate or the annotation does not exist yet (e.g. the first reconcile).
func coveredMembersPerCluster(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, memberCertName string) map[string]int {
	covered := map[string]int{}
	existing := &certmanagerv1.Certificate{}
	if err := c.Get(ctx, types.NamespacedName{Name: memberCertName, Namespace: res.GetNamespace()}, existing); err != nil {
		return covered
	}
	v, ok := existing.Annotations[coveredMembersPerClusterAnnotation]
	if !ok {
		return covered
	}
	_ = json.Unmarshal([]byte(v), &covered)
	return covered
}

// ensureSelfSignedCA idempotently creates the per-category self-signed CA chain used to
// sign a category's certs when user has not configured issuer for it. A SelfSigned Issuer
// bootstraps a self-signed CA Certificate, and a CA Issuer signs the leaves from it.
// All three objects are owned by the resource so they GC with it.
func ensureSelfSignedCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory) (int64, workflow.Status) {
	if status := ensureIssuer(ctx, c, res, selfSignedIssuerName(res, cat), certmanagerv1.IssuerSpec{
		IssuerConfig: certmanagerv1.IssuerConfig{
			SelfSigned: &certmanagerv1.SelfSignedIssuer{},
		},
	}); !status.IsOK() {
		return 0, status
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
	generation, status := ensureCertificate(ctx, c, res, caCert)
	if !status.IsOK() {
		return 0, status
	}

	if status := ensureIssuer(ctx, c, res, managedCAIssuerName(res, cat), certmanagerv1.IssuerSpec{
		IssuerConfig: certmanagerv1.IssuerConfig{
			CA: &certmanagerv1.CAIssuer{
				SecretName: managedCACertName(res, cat),
			},
		},
	}); !status.IsOK() {
		return 0, status
	}
	return generation, workflow.OK()
}

// ensureIssuer creates or updates an operator-owned cert-manager Issuer.
func ensureIssuer(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, name string, spec certmanagerv1.IssuerSpec) workflow.Status {
	existing := &certmanagerv1.Issuer{}
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: res.GetNamespace()}, existing)
	switch {
	case err == nil:
		existing.Spec = spec
		existing.OwnerReferences = certificateOwnerReferences(res)
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
				OwnerReferences: certificateOwnerReferences(res),
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
// mongod config fields, net.tls.CAFile (server CA) and net.tls.clusterCAFile (client CA).
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
	// The server bundle also keeps a stable ca-pem copy: it is the ConfigMap that client applications
	// are pointed at (MongoDBUser.status.clientCertificate.serverCAConfigMapName), so they can mount a
	// fixed key. mongod never uses it, the automation config points at the hash-named file.
	if err := ensureHashedCABundleConfigMap(ctx, c, res, ManagedServerCABundleConfigMapName(res.GetName()), serverBundle, tls.CAConfigMapKey); err != nil {
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
		if err := ensureHashedCABundleConfigMap(ctx, c, res, ManagedClientCABundleConfigMapName(res.GetName()), clientBundle, ""); err != nil {
			return workflow.Failed(err)
		}
	}

	log.Debugf("Populated operator-managed CA ConfigMaps for %s (server always; client: %t)", res.GetName(), hasClientCerts)
	return workflow.OK()
}

// ReadManagedCABundleHashes returns the hashes of the CA bundles currently published in the
// operator-managed CA ConfigMaps (their latestHash key).
func ReadManagedCABundleHashes(ctx context.Context, getter configmap.Getter, resourceName, namespace string, hasClientCerts bool) (string, string, error) {
	var serverCAHash, clientCAHash string
	var err error

	if serverCAHash, err = configmap.ReadKey(ctx, getter, util.LatestHashSecretKey, kube.ObjectKey(namespace, ManagedServerCABundleConfigMapName(resourceName))); err != nil {
		return "", "", err
	}

	if hasClientCerts {
		if clientCAHash, err = configmap.ReadKey(ctx, getter, util.LatestHashSecretKey, kube.ObjectKey(namespace, ManagedClientCABundleConfigMapName(resourceName))); err != nil {
			return "", "", err
		}
	}

	return serverCAHash, clientCAHash, nil
}

// ensureHashedCABundleConfigMap creates or updates the CA bundle ConfigMap with the given name.
// The ConfigMap stores the bundle under the hash of its content. The latestHash key holds this hash.
// The automation config sets net.tls.CAFile or clusterCAFile to <mount>/<hash>.
// When the bundle changes, the hash and the path also change. The agent then restarts the members.
//
// The ConfigMap also keeps the previous bundle under its own hash. The previousHash key holds this hash.
// The agent restarts the members one at a time. Some members use the old path until they restart.
// The previous bundle keeps the old path valid during the rotation. The -pem secrets use the same method.
//
// When aliasKey is not empty, the ConfigMap also stores the current bundle under aliasKey.
// Client applications use this fixed key (for example, ca-pem).
// The function writes the ConfigMap only when its data changes.
func ensureHashedCABundleConfigMap(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, name, bundle, aliasKey string) error {
	hash := enterprisepem.HashBytes([]byte(bundle))

	var existingData map[string]string
	existing, err := c.GetConfigMap(ctx, kube.ObjectKey(res.GetNamespace(), name))
	switch {
	case err == nil:
		existingData = existing.Data
	case !apiErrors.IsNotFound(err):
		return err
	}

	data := bundleDataWithPrevious(existingData, hash, bundle)
	if aliasKey != "" {
		data[aliasKey] = bundle
	}
	if err == nil && maps.Equal(existingData, data) {
		return nil
	}

	cm := configmap.Builder().
		SetName(name).
		SetNamespace(res.GetNamespace()).
		SetOwnerReferences(certificateOwnerReferences(res)).
		SetData(data).
		Build()
	return configmap.CreateOrUpdate(ctx, c, cm)
}

// bundleDataWithPrevious returns the ConfigMap data for a bundle: the bundle under its hash, the
// latestHash key and, after a change, the previous bundle under its hash with a previousHash key.
func bundleDataWithPrevious(existing map[string]string, hash, bundle string) map[string]string {
	data := map[string]string{hash: bundle, util.LatestHashSecretKey: hash}

	oldHash, ok := existing[util.LatestHashSecretKey]
	if !ok {
		return data
	}
	if oldHash == hash {
		// no rotation, keep whatever previous bundle is there
		data = maps.Clone(existing)
		data[hash] = bundle
		return data
	}
	if oldBundle, ok := existing[oldHash]; ok {
		data[util.PreviousHashSecretKey] = oldHash
		data[oldHash] = oldBundle
	}
	return data
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
	cert, found, status := readIssuedCertificate(ctx, c, res.GetNamespace(), secretName)
	if !status.IsOK() {
		return nil, nil, status
	}
	if !found {
		return nil, nil, workflow.Pending("Waiting for cert-manager to write the certificate secret %s", secretName)
	}
	return cert.Subject.Organization, cert.Subject.OrganizationalUnit, workflow.OK()
}

// readIssuedCertificate reads the leaf certificate cert-manager wrote into a TLS secret. found is
// false when the secret is not there yet.
func readIssuedCertificate(ctx context.Context, c kubernetesClient.Client, namespace, secretName string) (*x509.Certificate, bool, workflow.Status) {
	s, err := c.GetSecret(ctx, types.NamespacedName{Name: secretName, Namespace: namespace})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, false, workflow.OK()
		}
		return nil, false, workflow.Failed(err)
	}
	block, _ := pem.Decode(s.Data[corev1.TLSCertKey])
	if block == nil {
		return nil, false, workflow.Failed(xerrors.Errorf("could not PEM-decode tls.crt in secret %s", secretName))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, workflow.Failed(xerrors.Errorf("could not parse certificate in secret %s: %w", secretName, err))
	}
	return cert, true, workflow.OK()
}

// extendedKeyUsages maps the extended key usages the operator requests to the x509 ones.
var extendedKeyUsages = map[certmanagerv1.KeyUsage]x509.ExtKeyUsage{
	certmanagerv1.UsageServerAuth: x509.ExtKeyUsageServerAuth,
	certmanagerv1.UsageClientAuth: x509.ExtKeyUsageClientAuth,
}

// verifyKeyUsagesInIssuedCert checks that every issued certificate has the extended key usages the
// operator asked for.
//
// cert-manager does not check this: it compares the Certificate with its CertificateRequest, not with
// the certificate that was signed. An issuer that decides the usages itself can therefore drop one,
// and the Certificate still becomes Ready. The deployment then fails at the TLS handshake, with "unsupported certificate
// purpose" in the mongod logs and nothing on the resource, so it is checked here instead.
func verifyKeyUsagesInIssuedCert(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, required []certToEnsure) workflow.Status {
	for _, cert := range required {
		issued, found, status := readIssuedCertificate(ctx, c, res.GetNamespace(), cert.spec.SecretName)
		if !status.IsOK() {
			return status
		}
		if !found {
			return workflow.Pending("Waiting for cert-manager to write the certificate secret %s", cert.spec.SecretName)
		}
		for _, usage := range cert.spec.Usages {
			if isMissingEKU(issued, usage) {
				return workflow.Failed(xerrors.Errorf(
					"the certificate %s doesn't have the EKUs (%s) that were requested by operator, there are chances that configured issuer doesn't honor EKU requests. Point that category's issuerRef (security.managedCertificate.server or .client) at an issuer that allows the usage, or leave it unset so the operator signs the certificate with its own CA",
					cert.spec.SecretName, usage))
			}
		}
	}
	return workflow.OK()
}

// isMissingEKU reports whether the issued certificate is missing an extended key usage the operator
// asked for. It answers false for the requested usages that are not extended key usages, and for a
// certificate that allows every purpose, so the caller only reports what is really missing.
func isMissingEKU(issued *x509.Certificate, usage certmanagerv1.KeyUsage) bool {
	// usage here can be certmanagerv1.UsageDigitalSignature, UsageKeyEncipherment, UsageServerAuth or UsageClientAuth but we would like
	// to just verify the extended key usages that are UsageServerAuth and UsageClientAuth that's why we are looking up extendedKeyUsages[usage]
	eku, isExtendedUsage := extendedKeyUsages[usage]
	if !isExtendedUsage {
		return false
	}

	// the issued cert can be used for any purpose if the issued cert doesn't have any EKU or it has ExtKeyUsageAny
	if len(issued.ExtKeyUsage) == 0 || slices.Contains(issued.ExtKeyUsage, x509.ExtKeyUsageAny) {
		return false
	}

	// make sure issued cert's EKUs have the expected EKU
	return !slices.Contains(issued.ExtKeyUsage, eku)
}

// certificateReadyAtGeneration reports whether the Certificate's Ready condition describes both
// the Certificate's current spec and, when non-zero, the generation written by this preparation
// pass. A cached read can still serve the previous, internally consistent generation pair right
// after a write, so the written generation is a floor for both generations.
// Zero means no floor beyond the Certificate's own current generation.
func certificateReadyAtGeneration(cert *certmanagerv1.Certificate, writtenGeneration int64) bool {
	for _, cond := range cert.Status.Conditions {
		if cond.Type == certmanagerv1.CertificateConditionReady {
			return cond.Status == cmmeta.ConditionTrue &&
				cert.Generation >= writtenGeneration &&
				cond.ObservedGeneration >= writtenGeneration &&
				cond.ObservedGeneration >= cert.Generation
		}
	}
	return false
}
