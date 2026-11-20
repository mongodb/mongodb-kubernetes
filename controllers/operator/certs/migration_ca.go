package certs

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
)

// VerifyMigrationTrustsLegacyCA checks that a migration to operator-managed certificates keeps trusting
// the certificates the deployment already uses today, and fails with the value to set when it would not.
//
// The certificates in use are signed by the CA the user configured, while the managed ones are signed
// by the issuer in the managedCertificate block, which can be a different CA. During the migration the
// pods restart one at a time, so for a while the members that have moved to the new certificates and
// the ones still presenting the old ones talk to each other, and each side has to trust the other's
// certificates. If the managed trust bundle cannot validate the old certificates, the members reject
// each other until every pod has restarted, which is the outage the migration is meant to avoid.
//
// The CA file the user configured might have more than one CA certificate in it, for example the CA
// that signs the deployment's certificates and some public CAs. What's important is that the managed CA
// bundle can validate the legacy certificates, so the check reads them from the user's secrets and
// validates them against the managed certificate CA bundle. This would make sure that while transition
// legacy as well as new certs are trusted.
//
// The user keeps the old CA trusted by adding it to the category's additionalTrustedCAs. This check
// tells them so, rather than letting the deployment break while it restarts.
func VerifyMigrationTrustsLegacyCA(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	lastAchieved *mdbv1.Security,
	// componentNames are the names the certificate secrets are derived from.
	componentNames []string,
	log *zap.SugaredLogger,
) workflow.Status {
	legacyCACMName := legacyCAConfigMapName(lastAchieved)
	if legacyCACMName == "" {
		// The deployment trusted no CA of its own, so there is nothing that has to stay trusted.
		return workflow.OK()
	}

	// The members validate each other's member certificates against the server bundle, and their
	// clusterfiles against the client bundle.
	type check struct {
		bundle     string
		field      string
		secretName func(componentName string) string
	}
	checks := []check{
		{ManagedServerCABundleConfigMapName(res.GetName()), "server", lastAchieved.MemberCertificateSecretName},
	}
	if res.GetSecurity().RequiresX509ClientCerts() {
		checks = append(checks, check{ManagedClientCABundleConfigMapName(res.GetName()), "client", lastAchieved.InternalClusterAuthSecretName})
	}

	var problems []string
	for _, check := range checks {
		managedCertCAs, found, status := readTrustedCAs(ctx, c, res.GetNamespace(), check.bundle)
		if !status.IsOK() {
			return status
		}
		if !found {
			// The bundle is published with the managed certificates, which happens on a later reconcile
			// than the one that detects the migration. Nothing to compare yet.
			continue
		}
		for _, componentName := range componentNames {
			secretName := check.secretName(componentName)
			certs, status := readLegacyCertificates(ctx, c, res.GetNamespace(), secretName)
			if !status.IsOK() {
				return status
			}
			if len(certs) == 0 {
				// The deployment does not have this certificate, so there is nothing to validate.
				continue
			}
			// validatedBy makes sure the legacy leaf cert certs[0] can be trusted/validated by the managedCertCAs.
			// certs[1:] is passed to be treated as intermediate CAs, if users have configured any.
			if !validatedBy(certs[0], certs[1:], managedCertCAs) {
				problems = append(problems, fmt.Sprintf(
					"the certificate in secret %s is not signed by a CA in %s, so the members would reject it while they restart. Add the CA that signed it to spec.security.managedCertificate.%s.additionalTrustedCAs, for example %s",
					secretName, check.bundle, check.field, caRefSnippet(legacyCACMName)))
			}
		}
	}

	if len(problems) > 0 {
		message := "this migration would leave the CA the deployment trusts today out of the managed trust bundle, so its members would stop recognising each other while they restart: " + strings.Join(problems, "; ")
		log.Warnf("Refusing the migration to operator-managed certificates: %s", message)
		return workflow.Failed(xerrors.New(message))
	}
	return workflow.OK()
}

// readLegacyCertificates reads the certificates in the tls.crt of one of the user's secrets: the
// certificate first, then the intermediate CAs that follow it, if any. Nothing is returned when the
// secret is not there.
func readLegacyCertificates(ctx context.Context, c kubernetesClient.Client, namespace, secretName string) ([]*x509.Certificate, workflow.Status) {
	s, err := c.GetSecret(ctx, kube.ObjectKey(namespace, secretName))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, workflow.OK()
		}
		return nil, workflow.Failed(err)
	}
	return certificatesIn(string(s.Data[corev1.TLSCertKey])), workflow.OK()
}

// validatedBy reports whether a certificate can be validated by the managed CA bundle. The intermediates
// are the chain that follows the certificate in its file: they are used only to connect it to one of the
// bundle's CAs, and are not trusted themselves.
func validatedBy(cert *x509.Certificate, intermediates, managedCertCAs []*x509.Certificate) bool {
	roots := x509.NewCertPool()
	for _, ca := range managedCertCAs {
		roots.AddCert(ca)
	}
	intermediatePool := x509.NewCertPool()
	for _, ca := range intermediates {
		intermediatePool.AddCert(ca)
	}

	// What matters is which CA signed the certificate, not whether it is still valid, so it is checked
	// at a time it is valid: an expired certificate is not reported as untrusted.
	at := time.Now()
	if at.Before(cert.NotBefore) || at.After(cert.NotAfter) {
		at = cert.NotBefore
	}

	_, err := cert.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediatePool,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err == nil
}

// legacyCAConfigMapName returns the name of the ConfigMap the deployment trusts today, empty when it
// configured none.
func legacyCAConfigMapName(lastAchieved *mdbv1.Security) string {
	if lastAchieved == nil || lastAchieved.TLSConfig == nil {
		return ""
	}
	return lastAchieved.TLSConfig.CA
}

// readTrustedCAs returns the CA certificates the managed bundle ConfigMap publishes. All its values are
// read, so the way the bundle is stored does not matter, and a bundle kept from an earlier change counts:
// the pods still trust it.
func readTrustedCAs(ctx context.Context, c kubernetesClient.Client, namespace, name string) ([]*x509.Certificate, bool, workflow.Status) {
	cm, err := c.GetConfigMap(ctx, kube.ObjectKey(namespace, name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, false, workflow.OK()
		}
		return nil, false, workflow.Failed(err)
	}

	trusted := make([]*x509.Certificate, 0)
	for _, value := range cm.Data {
		trusted = append(trusted, certificatesIn(value)...)
	}
	return trusted, true, workflow.OK()
}

// certificatesIn returns the certificates a PEM value holds, ignoring values that are not certificates,
// such as the hash the bundle records.
func certificatesIn(value string) []*x509.Certificate {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	file := enterprisepem.NewFileFromData([]byte(value))
	certs, err := file.ParseCertificate()
	if err != nil {
		return nil
	}
	return certs
}

// caRefSnippet renders the additionalTrustedCAs entry that keeps a ConfigMap's CA trusted.
func caRefSnippet(configMapName string) string {
	return fmt.Sprintf(`{"name": %q, "kind": "ConfigMap", "key": %q}`, configMapName, tls.CAConfigMapKey)
}
