package certs

import (
	"context"
	"crypto/x509"
	"fmt"
	"slices"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// reasonManagedCARotated is the reason of the Issuing condition that the operator sets on the
// cert-manager Certificate of a leaf. The operator sets it when the self-signed CA that signs the
// leaf has rotated. cert-manager then issues the leaf again with the current CA.
// The MongoDB resource does not get this condition.
const reasonManagedCARotated = "ManagedCARotated"

// ReissueLeavesOnRotatedSelfSignedCA moves the leaves of every self-signed category onto the
// category's current root. cert-manager does not reissue a Certificate when the CA behind its
// Issuer changes, so after the operator's self-signed CA rotates the leaves keep being signed by
// the previous root while both roots are trusted (the bundle was widened by
// EnsureCertificatesAndCA). This asks cert-manager to reissue those leaves: it sets the
// Certificate's Issuing condition to True, the same thing `cmctl renew` does, and cert-manager then
// writes a new certificate into the same secret signed by the current root. A category is only moved
// once the members trust its current root (see currentCATrustedByBundle).
//
// The function only changes categories that the operator self-signed CA signs.
// With a user issuer, the CA that the operator reads (ca.crt or the ca input) can be different from
// the CA that signs the leaves. For example, an intermediate CA can sign the leaves while ca.crt has the root.
// Then no leaf matches, and the operator would issue the leaves again on each reconcile, without end.
// The user also controls the rotation of their own CA.
//
// Returns nil when managed certificates are off, and tolerates leaves/secrets/Certificates that are
// missing because they may be mid-issuance.
func ReissueLeavesOnRotatedSelfSignedCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	allOpts []Options,
	log *zap.SugaredLogger,
) error {
	sec := res.GetSecurity()
	if !sec.IsManagedCertificateEnabled() || len(allOpts) == 0 {
		return nil
	}

	categories := []certCategory{categoryServer}
	if sec.RequiresX509ClientCerts() {
		categories = append(categories, categoryClient)
	}

	for _, cat := range categories {
		if hasUserIssuer(res, cat) {
			continue
		}
		trusted, err := currentCATrustedByBundle(ctx, c, res, cat, defaultCABundleTarget(res, cat).name)
		if err != nil {
			return err
		}
		if !trusted {
			log.Infof("Not re-issuing the %s certificates of %s yet: the members do not trust the current self-signed %s CA. The next reconcile publishes it and rolls the members first", cat, res.GetName(), cat)
			continue
		}
		if err := reissueCategoryLeavesOnRotatedCA(ctx, c, res, cat, allOpts, log); err != nil {
			return err
		}
	}
	return nil
}

// currentCATrustedByBundle reports whether the current self-signed CA of a category (the root that
// is in the category's CA secret right now) is one of the CAs in the latest bundle of the CA bundle
// ConfigMap bundleName. A CA bundle is the set of CAs that members and clients trust, so a true
// result means that whoever runs with that bundle accepts leaves signed by the current CA.
// It returns false when the CA or the bundle does not exist yet.
//
// The caller runs after a successful reconcile, so the members run with the latest bundle of the
// ConfigMap. The CA can still rotate after that reconcile published the bundle. A leaf moved to the
// new CA at that point is signed by a CA the members do not trust yet. The next reconcile widens the
// bundle and rolls the members onto it, and the agents restart the members with the new bundle and
// the new leaf at the same time. A restarted secondary then presents a certificate that the primary
// rejects, the primary cannot step down, and the roll never completes.
func currentCATrustedByBundle(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, bundleName string) (bool, error) {
	currentPEM, status := readSelfSignedCACert(ctx, c, res, cat)
	if !status.IsOK() {
		return false, nil
	}
	current, err := parseCACerts(currentPEM)
	if err != nil {
		return false, fmt.Errorf("could not parse the current self-signed %s CA of %s: %w", cat, res.GetName(), err)
	}

	bundleCM, bundleCAs, err := readCABundleConfigMap(ctx, c, res, bundleName)
	if err != nil {
		return false, fmt.Errorf("could not read the %s CA bundle of %s: %w", cat, res.GetName(), err)
	}
	if bundleCM == nil {
		return false, nil
	}

	trusted := caFingerprints(bundleCAs)
	for _, fingerprint := range caFingerprints(current) {
		if !slices.Contains(trusted, fingerprint) {
			return false, nil
		}
	}
	return true, nil
}

// reissueCategoryLeavesOnRotatedCA reissues the leaves of one self-signed category that are not
// signed by the root currently in the category's CA secret.
func reissueCategoryLeavesOnRotatedCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	allOpts []Options,
	log *zap.SugaredLogger,
) error {
	currentPEM, status := readSelfSignedCACert(ctx, c, res, cat)
	if !status.IsOK() {
		// The CA secret is written by cert-manager. The caller only reaches this after a
		// successful reconcile, so a missing root means it is being reissued right now; there
		// is nothing to compare against until it comes back.
		log.Warnf("Skipping leaf re-issue for the %s category of %s: %s", cat, res.GetName(), status.Message())
		return nil
	}

	currentCAs, err := parseCACerts(currentPEM)
	if err != nil {
		return fmt.Errorf("could not parse the current self-signed %s CA of %s: %w", cat, res.GetName(), err)
	}
	issuers := parseIssuers(currentCAs)

	leaves, err := categoryIssuedLeaves(ctx, c, res, cat, allOpts)
	if err != nil {
		return err
	}
	for _, leaf := range leaves {
		if err := reissueLeafIfSignedByPreviousCA(ctx, c, res, cat, leaf, issuers, log); err != nil {
			return err
		}
	}
	return nil
}

// categoryIssuedLeaves returns every leaf certificate that the issuer of a category signs: the
// certificates of the resource, whose Certificate is named after their secret, and the client
// certificates of its MongoDBUsers.
func categoryIssuedLeaves(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, cat certCategory, allOpts []Options) ([]issuedLeaf, error) {
	var leaves []issuedLeaf
	for _, secretName := range categoryLeafSecrets(res, cat, allOpts) {
		leaves = append(leaves, issuedLeaf{certName: secretName, secretName: secretName})
	}
	userLeaves, err := userClientCertificateLeaves(ctx, c, res, cat)
	if err != nil {
		return nil, err
	}
	return append(leaves, userLeaves...), nil
}

// categoryLeafNeedsReissue reports whether any leaf of the passed self-signed category (`cat`) is
// not signed by the root currently in the category's CA secret. It returns false when the root
// cannot be read yet because cert-manager is still writing it.
func categoryLeafNeedsReissue(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	allOpts []Options,
	log *zap.SugaredLogger,
) (bool, error) {
	currentPEM, status := readSelfSignedCACert(ctx, c, res, cat)
	if !status.IsOK() {
		log.Debugf("Skipping the re-issue check for the %s category of %s: %s", cat, res.GetName(), status.Message())
		return false, nil
	}

	currentCAs, err := parseCACerts(currentPEM)
	if err != nil {
		return false, fmt.Errorf("could not parse the current self-signed %s CA of %s: %w", cat, res.GetName(), err)
	}
	issuers := parseIssuers(currentCAs)

	leafSecrets := categoryLeafSecrets(res, cat, allOpts)
	for _, secretName := range leafSecrets {
		leaf, err := readLeafCertificate(ctx, c, res.GetNamespace(), secretName)
		if err != nil {
			// The secret may not exist yet while the certificate is being issued.
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if !signedByAny(leaf, issuers) {
			return true, nil
		}
	}
	return false, nil
}

// reissueLeafIfSignedByPreviousCA sets Issuing=True on one leaf Certificate unless its secret is
// already signed by the current root or a reissue is already in progress.
func reissueLeafIfSignedByPreviousCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	issued issuedLeaf,
	issuers []*x509.Certificate,
	log *zap.SugaredLogger,
) error {
	leaf, err := readLeafCertificate(ctx, c, res.GetNamespace(), issued.secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Skipping re-issue of %s: its certificate secret %s does not exist yet", issued.certName, issued.secretName)
			return nil
		}
		return err
	}
	if signedByAny(leaf, issuers) {
		return nil
	}

	cert := &certmanagerv1.Certificate{}
	if err := c.Get(ctx, types.NamespacedName{Name: issued.certName, Namespace: res.GetNamespace()}, cert); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Skipping re-issue of %s: its Certificate does not exist yet", issued.certName)
			return nil
		}
		return err
	}
	if isConditionIssuing(cert) {
		return nil
	}

	setConditionIssuing(cert, metav1.Now(), reasonManagedCARotated,
		fmt.Sprintf("The certificate is not signed by the current self-signed %s CA; re-issuing it after the CA rotation", cat))

	log.Infof("Re-issuing certificate %s/%s: it is not signed by the current self-signed %s CA, which has rotated", res.GetNamespace(), cert.Name, cat)
	return c.Status().Update(ctx, cert)
}

// isConditionIssuing reports whether the Certificate already has Issuing=True, in which case a
// reissue is already in progress and setting the condition again would be a no-op update.
func isConditionIssuing(cert *certmanagerv1.Certificate) bool {
	for _, cond := range cert.Status.Conditions {
		if cond.Type == certmanagerv1.CertificateConditionIssuing {
			return cond.Status == cmmeta.ConditionTrue
		}
	}
	return false
}

// setConditionIssuing upserts the Issuing condition on a Certificate, keeping any other condition
// (notably Ready) intact.
func setConditionIssuing(cert *certmanagerv1.Certificate, now metav1.Time, reason, message string) {
	condition := certmanagerv1.CertificateCondition{
		Type:               certmanagerv1.CertificateConditionIssuing,
		Status:             cmmeta.ConditionTrue,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: &now,
		ObservedGeneration: cert.Generation,
	}
	for i, cond := range cert.Status.Conditions {
		if cond.Type == certmanagerv1.CertificateConditionIssuing {
			cert.Status.Conditions[i] = condition
			return
		}
	}
	cert.Status.Conditions = append(cert.Status.Conditions, condition)
}
