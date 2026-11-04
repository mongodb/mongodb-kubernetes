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

	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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

// ensureCategoryBundle publishes the trust bundle of a category.
func ensureCategoryBundle(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	allOpts []Options,
) (retained, refused bool, err error) {
	issuedSecretName, leafSecrets := categorySecrets(res, cat, allOpts)
	primary, err := primaryTrustedCA(ctx, c, res, cat, issuedSecretName)
	if err != nil {
		return false, false, err
	}

	additional, err := additionalTrustedCAs(ctx, c, res, cat)
	if err != nil {
		return false, false, err
	}

	name := ManagedServerCABundleConfigMapName(res.GetName())
	if cat == categoryClient {
		name = ManagedClientCABundleConfigMapName(res.GetName())
	}
	publishedCM, published, err := publishedTrustedCAs(ctx, c, res, name)
	if err != nil {
		return false, false, err
	}

	previous := retainedTrustedCAs(published, primary)

	prune, err := prunePreviousCA(ctx, c, res, cat, leafSecrets, previous)
	if err != nil {
		return false, false, err
	}
	if prune {
		previous = nil
	}

	// The server bundle also keeps a stable ca-pem copy: it is the ConfigMap that client applications
	// are pointed at (MongoDBUser.status.clientCertificate.serverCAConfigMapName), so they can mount a
	// fixed key.
	aliasKey := ""
	if cat == categoryServer {
		aliasKey = tls.CAConfigMapKey
	}
	if err = ensureHashedBundleConfigMap(ctx, c, res, name, publishedCM, mergeCABundle(primary, previous, additional), aliasKey); err != nil {
		return false, false, err
	}

	retained = len(previous) > 0
	refused = pruneRequested(res, cat) && retained
	return retained, refused, nil
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

// ensureHashedBundleConfigMap publishes the bundle in the ConfigMap with the given name under its
// content hash, keeping the previously published bundle, and returns the hash. When aliasKey is set
// the current bundle is also stored under that fixed key. The ConfigMap is only written when its
// data changes.
func ensureHashedBundleConfigMap(
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
	if existing != nil && maps.Equal(existingData, data) {
		return nil
	}

	cm := configmap.Builder().
		SetName(name).
		SetNamespace(res.GetNamespace()).
		SetOwnerReferences(kube.BaseOwnerReference(res)).
		SetData(data).
		Build()

	return configmap.CreateOrUpdate(ctx, c, cm)
}

// primaryTrustedCA returns the primary CA of a category.
func primaryTrustedCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, issuedSecretName string) ([]caCert, error) {
	primaryPEM, status := resolveCategoryCA(ctx, c, res, cat, issuedSecretName)
	if !status.IsOK() {
		return nil, errors.New(status.Message())
	}
	primary, err := parseCACerts(primaryPEM)
	if err != nil {
		return nil, fmt.Errorf("the %s CA is not a valid PEM certificate: %w", cat, err)
	}

	return primary, nil
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

// publishedTrustedCAs returns the CAs of the bundle currently published in the ConfigMap of a category.
func publishedTrustedCAs(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, name string) (*corev1.ConfigMap, []caCert, error) {
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

// retainedTrustedCAs returns the CAs of the bundle currently published in the ConfigMap of a category that are not in the primary CAs.
func retainedTrustedCAs(published []caCert, primary []caCert) []caCert {
	current := strings.Join(caFingerprints(primary), ",")
	previous := strings.Join(caFingerprints(published), ",")

	if current == previous {
		return nil
	}

	previousFingerprints := strings.Split(previous, ",")
	var found []caCert
	for _, ca := range published {
		if slices.Contains(previousFingerprints, ca.fingerprint) {
			found = append(found, ca)
		}
	}

	currentFingerprints := strings.Split(current, ",")
	var retained []caCert
	for _, ca := range found {
		if !slices.Contains(currentFingerprints, ca.fingerprint) {
			retained = append(retained, ca)
		}
	}

	if len(retained) > 0 {
		return retained
	}

	return nil
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
func prunePreviousCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, leafSecrets []string, retained []caCert) (bool, error) {
	if len(retained) == 0 || !pruneRequested(res, cat) {
		return false, nil
	}

	signed, err := leafSignedByCA(ctx, c, res.GetNamespace(), leafSecrets, retained)
	if err != nil {
		return false, fmt.Errorf("prune of the previous %s CA refused: %v", cat, err)
	}

	return !signed, nil
}

// pruneRequested reports whether the resource asks to prune the previous CA of the category, with
// the prune annotation set to "true" (both categories) or to the category name.
func pruneRequested(res CertificateOwner, cat certCategory) bool {
	value := res.GetAnnotations()[util.PrunePreviousCAAnnotation]

	return value == "true" || value == string(cat)
}

// leafSignedByCA reports whether any of the issued certificate secrets is signed by any of the CAs.
func leafSignedByCA(ctx context.Context, c kubernetesClient.Client, namespace string, secretNames []string, cas []caCert) (bool, error) {
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

// categorySecrets returns, for a category, the issued secret the issuer's CA is read from and the
// secrets of every issued leaf certificate of that category.
func categorySecrets(res CertificateOwner, cat certCategory, allOpts []Options) (string, []string) {
	if cat == categoryClient {
		return clientIssuedSecretName(res, allOpts[0]), clientLeafSecretNames(res, allOpts)
	}
	return allOpts[0].CertSecretName, serverLeafSecretNames(allOpts)
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
