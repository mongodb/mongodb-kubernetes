package certs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// caCert is one CA certificate of a trust bundle, identified by the SHA-256 of its DER encoding.
type caCert struct {
	fingerprint string
	der         []byte
}

// caBundleTarget is the ConfigMap a category's trust bundle is published to, and the fixed key
// that also holds the current bundle (empty for none).
type caBundleTarget struct {
	name     string
	aliasKey string
}

// defaultCABundleTarget returns the ConfigMap for the trust bundle of a category.
// The server bundle also has the fixed ca-pem key. MongoDBUser.status.clientCertificate.serverCAConfigMapName
// points client applications to this ConfigMap, so they can mount a key that does not change.
// mongod does not use ca-pem. The automation config points to the file named by the hash.
// No client application uses the client bundle, so it has no fixed key.
func defaultCABundleTarget(res CertificateOwner, cat certCategory) caBundleTarget {
	if cat == categoryClient {
		return caBundleTarget{name: ManagedClientCABundleConfigMapName(res.GetName())}
	}
	return caBundleTarget{name: ManagedServerCABundleConfigMapName(res.GetName()), aliasKey: tls.CAConfigMapKey}
}

// ensureCategoryBundle publishes the trust bundle of a category: its current CA, the previous
// CA when the CA was rotated and the prune annotation did not drop it yet, and the additionalTrustedCAs.
// It returns true when the bundle still has a previous CA.
func ensureCategoryBundle(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	allOpts []Options,
	bundleTarget caBundleTarget,
) (bool, error) {
	leafSecrets := categoryLeafSecrets(res, cat, allOpts)
	current, err := currentCA(ctx, c, res, cat, leafSecrets)
	if err != nil {
		return false, err
	}

	additional, err := additionalTrustedCAs(ctx, c, res, cat)
	if err != nil {
		return false, err
	}

	bundleCM, bundleCAs, err := readCABundleConfigMap(ctx, c, res, bundleTarget.name)
	if err != nil {
		return false, err
	}

	previous := previousCAs(bundleCAs, current, additional)

	prune, err := prunePreviousCA(ctx, c, res, cat, leafSecrets, previous)
	if err != nil {
		return false, err
	}
	if prune {
		previous = nil
	}

	if err = ensureHashedCABundleConfigMap(
		ctx,
		c,
		res,
		bundleTarget.name,
		bundleCM,
		mergeCABundle(current, previous, additional), bundleTarget.aliasKey); err != nil {
		return false, err
	}

	return len(previous) > 0, nil
}

// mergeCABundle builds the PEM bundle of all the given CAs. Each CA appears once (deduplicated by
// fingerprint) and the order is by fingerprint, so the same set of CAs always gives the same bundle (hash).
func mergeCABundle(groups ...[]caCert) string {
	byFingerprint := map[string]caCert{}
	for _, group := range groups {
		for _, ca := range group {
			byFingerprint[ca.fingerprint] = ca
		}
	}
	var bundle strings.Builder
	for _, fingerprint := range slices.Sorted(maps.Keys(byFingerprint)) {
		bundle.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: byFingerprint[fingerprint].der}))
	}
	return bundle.String()
}

// now is the clock used to timestamp bundle publications. Tests replace it to control the age of a
// published bundle.
var now = time.Now

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
// The function writes the ConfigMap only when its data changes or when the publication timestamp is missing.
func ensureHashedCABundleConfigMap(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	name string,
	existing *corev1.ConfigMap,
	bundle, aliasKey string,
) error {
	hash := enterprisepem.HashBytes([]byte(bundle))

	var existingData map[string]string
	if existing != nil {
		existingData = existing.Data
	}
	data := bundleDataWithPrevious(existingData, hash, bundle)
	if aliasKey != "" {
		data[aliasKey] = bundle
	}

	publishedAt, publishedAtChanged := caBundlePublishedAt(existing, hash)
	if existing != nil && maps.Equal(existingData, data) && !publishedAtChanged {
		return nil
	}

	annotations := map[string]string{}
	if existing != nil {
		maps.Copy(annotations, existing.Annotations)
	}
	annotations[util.CABundlePublishedAtAnnotation] = publishedAt

	cm := configmap.Builder().
		SetName(name).
		SetNamespace(res.GetNamespace()).
		SetOwnerReferences(certificateOwnerReferences(res)).
		SetData(data).
		SetAnnotations(annotations).
		Build()

	return configmap.CreateOrUpdate(ctx, c, cm)
}

// caBundlePublishedAt returns the value of the published-at annotation and whether it changed. The
// timestamp is kept while the bundle's latestHash is unchanged, and refreshed when a new bundle is
// published. An existing ConfigMap without the annotation is treated as just published.
func caBundlePublishedAt(existing *corev1.ConfigMap, hash string) (string, bool) {
	if existing != nil && existing.Data[util.LatestHashSecretKey] == hash {
		if publishedAt := existing.Annotations[util.CABundlePublishedAtAnnotation]; publishedAt != "" {
			return publishedAt, false
		}
	}
	return now().UTC().Format(time.RFC3339), true
}

// currentCA returns the current CA of a category. For a user issuer, it reads the CA from the first leaf secret.
func currentCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, leafSecrets []string) ([]caCert, error) {
	issuedSecretName := ""
	if len(leafSecrets) > 0 {
		issuedSecretName = leafSecrets[0]
	}
	currentPEM, status := resolveCategoryCA(ctx, c, res, cat, issuedSecretName)
	if !status.IsOK() {
		return nil, errors.New(status.Message())
	}
	current, err := parseCACerts(currentPEM)
	if err != nil {
		return nil, fmt.Errorf("the %s CA is not a valid PEM certificate: %w", cat, err)
	}

	return current, nil
}

// additionalTrustedCAs reads the additional trusted CAs of a category.
func additionalTrustedCAs(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory) ([]caCert, error) {
	cfg := certCategoryConfig(res, cat)
	if cfg == nil {
		return nil, nil
	}
	var additional []caCert
	for _, src := range cfg.AdditionalTrustedCAs {
		pemData, status := readCASource(ctx, c, res, src)
		if !status.IsOK() {
			return nil, errors.New(status.Message())
		}
		parsed, err := parseCACerts(pemData)
		if err != nil {
			return nil, fmt.Errorf("additional trusted CA %s %q (key %q) is not a valid PEM certificate: %w", src.Kind, src.Name, src.Key, err)
		}
		additional = append(additional, parsed...)
	}

	return additional, nil
}

// readCABundleConfigMap returns the CA bundle ConfigMap with the given name and the CAs of its latest bundle.
// It returns nil and no CAs when the ConfigMap does not exist.
func readCABundleConfigMap(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, name string) (*corev1.ConfigMap, []caCert, error) {
	cm, err := c.GetConfigMap(ctx, kube.ObjectKey(res.GetNamespace(), name))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}

		return nil, nil, err
	}

	caCerts, err := parseCACerts(cm.Data[cm.Data[util.LatestHashSecretKey]])
	if err != nil {
		return nil, nil, err
	}

	return &cm, caCerts, nil
}

// parseCACerts returns the CERTIFICATE blocks found in the PEM data. It fails when there are none.
func parseCACerts(pemData string) ([]caCert, error) {
	var parsed []caCert
	rest := []byte(pemData)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		parsed = append(parsed, caCert{fingerprint: hex.EncodeToString(sum[:]), der: block.Bytes})
	}
	if len(parsed) == 0 {
		return nil, xerrors.New("no PEM encoded certificate found")
	}
	return parsed, nil
}

// previousCAs returns the CAs that are in the CA bundle ConfigMap now, but are not the current CA
// and are not in additionalTrustedCAs. After a CA rotation, the bundle keeps the old CA until the
// prune annotation removes it. Members with a certificate from the old CA can then still connect
// during the rotation. A CA that is removed from additionalTrustedCAs also stays until a prune.
func previousCAs(bundleCAs, current, additional []caCert) []caCert {
	keep := caFingerprints(slices.Concat(current, additional))
	var previous []caCert
	for _, ca := range bundleCAs {
		if !slices.Contains(keep, ca.fingerprint) {
			previous = append(previous, ca)
		}
	}
	return previous
}

// caFingerprints returns the sorted, unique fingerprints of the CAs.
func caFingerprints(cas []caCert) []string {
	fingerprints := make([]string, 0, len(cas))
	for _, ca := range cas {
		fingerprints = append(fingerprints, ca.fingerprint)
	}
	slices.Sort(fingerprints)
	return slices.Compact(fingerprints)
}

// prunePreviousCA returns true if the previous CA of the category should be pruned.
func prunePreviousCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, leafSecrets []string, previous []caCert) (bool, error) {
	if len(previous) == 0 || !pruneRequested(res, cat) {
		return false, nil
	}

	signed, err := areLeavesSignedByCA(ctx, c, res.GetNamespace(), leafSecrets, previous)
	if err != nil {
		return false, fmt.Errorf("cannot check if the leaf certificates are signed by the previous %s CA: %w", cat, err)
	}
	if signed {
		return false, nil
	}

	userLeaves, err := userClientCertificateLeaves(ctx, c, res, cat)
	if err != nil {
		return false, err
	}
	signed, err = areUserLeavesSignedByCA(ctx, c, res.GetNamespace(), userLeaves, previous)
	if err != nil {
		return false, fmt.Errorf("cannot check if the MongoDBUser client certificates are signed by the previous %s CA: %w", cat, err)
	}

	return !signed, nil
}

// pruneRequested reports whether the resource asks to prune the previous CA of the category, with
// the prune annotation set to "true" (both categories) or to the category name. Owners that expose
// a component target one component instead: the annotation is "true" (every component) or a
// comma-separated list of components.
func pruneRequested(res CertificateOwner, cat certCategory) bool {
	value := res.GetAnnotations()[util.PrunePreviousCAAnnotation]
	if component := ownerPruneComponent(res); component != "" {
		return slices.Contains(PruneRequestedComponents(value), component)
	}

	return value == "true" || value == string(cat)
}

// areLeavesSignedByCA reports whether any of the issued certificate secrets is signed by any of the CAs.
func areLeavesSignedByCA(ctx context.Context, c kubernetesClient.Client, namespace string, secretNames []string, cas []caCert) (bool, error) {
	issuers := parseIssuers(cas)
	for _, name := range secretNames {
		leaf, err := readLeafCertificate(ctx, c, namespace, name)
		if err != nil {
			return false, err
		}
		if signedByAny(leaf, issuers) {
			return true, nil
		}
	}

	return false, nil
}

// areUserLeavesSignedByCA reports whether any of the MongoDBUser client certificates is signed by any
// of the CAs. A certificate whose secret does not exist yet is skipped: cert-manager has not issued
// it, so no application uses it, and it will be signed by the current CA.
func areUserLeavesSignedByCA(ctx context.Context, c kubernetesClient.Client, namespace string, leaves []issuedLeaf, cas []caCert) (bool, error) {
	issuers := parseIssuers(cas)
	for _, l := range leaves {
		leaf, err := readLeafCertificate(ctx, c, namespace, l.secretName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if signedByAny(leaf, issuers) {
			return true, nil
		}
	}

	return false, nil
}

// signedByAny returns true if the leaf is signed by any of the issuers.
func signedByAny(leaf *x509.Certificate, issuers []*x509.Certificate) bool {
	for _, issuer := range issuers {
		if bytes.Equal(leaf.RawIssuer, issuer.RawSubject) &&
			issuer.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil {
			return true
		}
	}
	return false
}

// parseIssuers returns the x509.Certificates of the CAs.
func parseIssuers(cas []caCert) []*x509.Certificate {
	var issuers []*x509.Certificate
	for _, ca := range cas {
		if parsed, err := x509.ParseCertificate(ca.der); err == nil {
			issuers = append(issuers, parsed)
		}
	}

	return issuers
}

// readLeafCertificate returns the leaf (first certificate of tls.crt) of an issued certificate secret.
func readLeafCertificate(ctx context.Context, c kubernetesClient.Client, namespace, secretName string) (*x509.Certificate, error) {
	secret, err := c.GetSecret(ctx, types.NamespacedName{Name: secretName, Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("could not read certificate secret %s: %w", secretName, err)
	}

	block, _ := pem.Decode(secret.Data[corev1.TLSCertKey])
	if block == nil {
		return nil, fmt.Errorf("could not PEM-decode tls.crt of certificate secret %s", secretName)
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("could not parse tls.crt of certificate secret %s: %w", secretName, err)
	}

	return leaf, nil
}

// categoryLeafSecrets returns the secrets of every issued leaf certificate of a category.
// For a user issuer, the operator reads the CA of the category from ca.crt of the first secret.
func categoryLeafSecrets(res CertificateOwner, cat certCategory, allOpts []Options) []string {
	if cat == categoryClient {
		return clientLeafSecretNames(res, allOpts)
	}
	return serverLeafSecretNames(allOpts)
}

// serverLeafSecretNames are the secrets of the issued certificates signed by the server category issuer.
func serverLeafSecretNames(allOpts []Options) []string {
	names := make([]string, 0, len(allOpts))
	for _, opts := range allOpts {
		names = append(names, opts.CertSecretName)
	}
	return names
}

// clientLeafSecretNames are the secrets of the issued certificates signed by the client category
// issuer: the internal-cluster clusterFile certs and the agent cert.
func clientLeafSecretNames(res CertificateOwner, allOpts []Options) []string {
	sec := res.GetSecurity()
	var names []string
	if sec.GetInternalClusterAuthenticationMode() == util.X509 {
		for _, opts := range allOpts {
			if opts.InternalClusterSecretName != "" {
				names = append(names, opts.InternalClusterSecretName)
			}
		}
	}
	if sec.GetAgentMechanism("") == util.X509 {
		names = append(names, sec.AgentClientCertificateSecretName(res.GetName()))
	}
	return names
}

// issuedLeaf is a leaf certificate that the operator issues: the cert-manager Certificate and the
// secret cert-manager writes it to. The names differ for the MongoDBUser client certificates.
type issuedLeaf struct {
	certName   string
	secretName string
}

// userClientCertificateLeaves returns the x509 client certificates that the operator issues for the
// MongoDBUsers of res (see ClientCertificate) and that the issuer of the category signs. A user
// belongs to res when it sets spec.clientCertificate and its spec.mongodbResourceRef points to res.
// The user controller only issues the certificate when the user is in the namespace of res, so only
// that namespace is listed. A user that is being deleted is ignored: its access goes away with it.
// Only MongoDB and MongoDBMultiCluster resources have users. For the other owners (the Ops Manager
// components) it returns no leaves and does not read the users.
func userClientCertificateLeaves(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory) ([]issuedLeaf, error) {
	switch res.(type) {
	case *mdbv1.MongoDB, *mdbmulti.MongoDBMultiCluster:
	default:
		return nil, nil
	}
	sec := res.GetSecurity()
	if !sec.IsManagedCertificateEnabled() || !sec.Authentication.IsX509Enabled() || clientCertificateSigner(sec) != cat {
		return nil, nil
	}

	users := &userv1.MongoDBUserList{}
	if err := c.List(ctx, users, client.InNamespace(res.GetNamespace())); err != nil {
		// Without the MongoDBUser CRD there are no users.
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not list the MongoDBUsers of %s: %w", res.GetName(), err)
	}

	var leaves []issuedLeaf
	for i := range users.Items {
		user := &users.Items[i]
		if user.Spec.ClientCertificate == nil || !user.DeletionTimestamp.IsZero() || !userRefersTo(user, res) {
			continue
		}
		leaves = append(leaves, issuedLeaf{certName: user.ClientCertificateName(), secretName: user.ClientCertificateSecretName()})
	}

	return leaves, nil
}

// UserClientCertificateSecretNames returns the secrets of the x509 client certificates that the
// operator issues for the MongoDBUsers of res (see userClientCertificateLeaves). The MongoDBUser owns
// these certificates, so the controller of res watches the secrets to retry a refused prune of the
// previous CA once the user certificates move to the new CA.
func UserClientCertificateSecretNames(ctx context.Context, c kubernetesClient.Client, res CertificateOwner) ([]string, error) {
	var names []string
	for _, cat := range []certCategory{categoryServer, categoryClient} {
		leaves, err := userClientCertificateLeaves(ctx, c, res, cat)
		if err != nil {
			return nil, err
		}
		for _, l := range leaves {
			names = append(names, l.secretName)
		}
	}
	return names, nil
}

// userRefersTo reports whether the spec.mongodbResourceRef of the user points to res. The namespace
// of the reference defaults to the namespace of the user, as in the user controller.
func userRefersTo(user *userv1.MongoDBUser, res CertificateOwner) bool {
	ref := user.Spec.MongoDBResourceRef
	namespace := ref.Namespace
	if namespace == "" {
		namespace = user.Namespace
	}
	return ref.Name == res.GetName() && namespace == res.GetNamespace()
}
