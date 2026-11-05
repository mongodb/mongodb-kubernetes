package certs

import (
	"context"
	"crypto/x509"
	"fmt"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// reasonManagedCARotated is the reason set on the Issuing condition when the operator asks
// cert-manager to reissue a leaf because the self-signed CA that signs it has been rotated.
const reasonManagedCARotated = "ManagedCARotated"

// ReissueLeavesOnRotatedSelfSignedCA moves the leaves of every self-signed category onto the
// category's current root. cert-manager does not reissue a Certificate when the CA behind its
// Issuer changes, so after the operator's self-signed CA rotates the leaves keep being signed by
// the previous root while both roots are trusted (the bundle was widened by
// EnsureCertificatesAndCA). This asks cert-manager to reissue those leaves: it sets the
// Certificate's Issuing condition to True, the same thing `cmctl renew` does, and cert-manager then
// writes a new certificate into the same secret signed by the current root.
//
// It only acts on categories signed by the operator's self-signed CA. With a user issuer the CA in
// the issuer's secret may legitimately differ from the one the issuer actually signs with, and
// reissuing on that mismatch would loop forever. Returns nil when managed certificates are off, and
// tolerates leaves/secrets/Certificates that are missing because they may be mid-issuance.
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
		if err := reissueCategoryLeavesOnRotatedCA(ctx, c, res, cat, allOpts, log); err != nil {
			return err
		}
	}
	return nil
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

	_, leafSecrets := categorySecrets(res, cat, allOpts)
	for _, secretName := range leafSecrets {
		if err := reissueLeafIfSignedByPreviousCA(ctx, c, res, cat, secretName, issuers, log); err != nil {
			return err
		}
	}
	return nil
}

// reissueLeafIfSignedByPreviousCA sets Issuing=True on one leaf Certificate unless its secret is
// already signed by the current root or a reissue is already in progress.
func reissueLeafIfSignedByPreviousCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	cat certCategory,
	secretName string,
	issuers []*x509.Certificate,
	log *zap.SugaredLogger,
) error {
	leaf, err := readLeafCertificate(ctx, c, res.GetNamespace(), secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Skipping re-issue of %s: its certificate secret does not exist yet", secretName)
			return nil
		}
		return err
	}
	if signedByAny(leaf, issuers) {
		return nil
	}

	cert := &certmanagerv1.Certificate{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: res.GetNamespace()}, cert); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Skipping re-issue of %s: its Certificate does not exist yet", secretName)
			return nil
		}
		return err
	}
	if certificateIssuing(cert) {
		return nil
	}

	setCertificateIssuing(cert, metav1.Now(), reasonManagedCARotated,
		fmt.Sprintf("The certificate is not signed by the current self-signed %s CA; re-issuing it after the CA rotation", cat))

	log.Infof("Re-issuing certificate %s/%s: it is not signed by the current self-signed %s CA, which has rotated", res.GetNamespace(), cert.Name, cat)
	return c.Status().Update(ctx, cert)
}

// certificateIssuing reports whether the Certificate already has Issuing=True, in which case a
// reissue is already in progress and setting the condition again would be a no-op update.
func certificateIssuing(cert *certmanagerv1.Certificate) bool {
	for _, cond := range cert.Status.Conditions {
		if cond.Type == certmanagerv1.CertificateConditionIssuing {
			return cond.Status == cmmeta.ConditionTrue
		}
	}
	return false
}

// setCertificateIssuing upserts the Issuing condition on a Certificate, keeping any other condition
// (notably Ready) intact.
func setCertificateIssuing(cert *certmanagerv1.Certificate, now metav1.Time, reason, message string) {
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
