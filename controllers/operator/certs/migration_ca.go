package certs

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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
	return caRefSnippetForKey(configMapName, tls.CAConfigMapKey)
}

// MigrationTrustCheck is the material of one component's legacy-to-managed migration that has to
// be checked before the workload changes: the certificate the deployment uses today, the active
// managed CA bundle, the certificate the operator issued and the legacy CA ConfigMap.
type MigrationTrustCheck struct {
	// Component names the workload in errors, for example "Ops Manager" or "AppDB".
	Component string
	// LegacyCertificateSecretName locates the certificate the deployment uses today.
	LegacyCertificateSecretName string
	// LegacyCertificateKey selects the certificate file from that Secret. It is needed for the
	// Ops Manager legacy form, which follows the Secret type: kubernetes.io/tls uses tls.crt,
	// anything else is the raw server.pem file the pod mounts.
	LegacyCertificateKey func(corev1.Secret) string
	// LegacyPEM pins the exact PEM to validate instead of reading LegacyCertificateSecretName.
	LegacyPEM string
	// LegacyCertificateIsOperatorPEM selects the active file of an operator-generated -pem Secret.
	// The AppDB migration retains that file, not the original TLS Secret, in the managed -pem Secret.
	LegacyCertificateIsOperatorPEM bool
	// ManagedCABundleName is the managed bundle the workload selects. Only its active bundle is
	// read, not a previous bundle retained for a rotation.
	ManagedCABundleName string
	// IssuedCertificateSecretName is the certificate cert-manager wrote for the managed leaf.
	IssuedCertificateSecretName string
	// LegacyCAConfigMapName and LegacyCAConfigMapKey are the CA file the deployment trusts today.
	LegacyCAConfigMapName string
	LegacyCAConfigMapKey  string
	// AdditionalTrustedCAsField is the field that widens the managed bundle, named in errors.
	AdditionalTrustedCAsField string
}

// VerifyManagedCertificateMigrationTrust checks that the active managed bundle trusts the legacy
// certificate before a workload is changed, matching VerifyMigrationTrustsLegacyCA. It does not
// require the legacy CA to trust the new issuer. Pods that have not restarted may reject the new
// leaf until they roll onto the widened bundle, which is the MongoDB behavior. It compares
// certificates and configured trust only; it does not prove that a client reloaded a bundle.
//
// A check that already holds the legacy PEM validates those bytes. The AppDB seeder uses that form
// so it checks the certificate it is about to copy rather than rereading a Secret that may have
// changed. LegacyCAConfigMapName and LegacyCAConfigMapKey are named in the refusal, not verified.
func VerifyManagedCertificateMigrationTrust(ctx context.Context, c client.Reader, namespace string, check MigrationTrustCheck) workflow.Status {
	withComponent := func(status workflow.Status) workflow.Status {
		if status.IsOK() {
			return status
		}
		return workflow.Failed(xerrors.Errorf("%s cannot migrate to operator-managed certificates: %s", check.Component, status.Message()))
	}

	managedCAs, status := readActiveTrustedCAs(ctx, c, namespace, check.ManagedCABundleName)
	if !status.IsOK() {
		return withComponent(status)
	}

	var legacyCerts []*x509.Certificate
	if check.LegacyPEM != "" {
		legacyCerts = certificatesIn(check.LegacyPEM)
		if len(legacyCerts) == 0 {
			return workflow.Failed(xerrors.Errorf("%s cannot migrate to operator-managed certificates: the published certificate file in Secret %s is empty or invalid", check.Component, check.LegacyCertificateSecretName))
		}
	} else {
		var status workflow.Status
		legacyCerts, status = readMigrationCertificate(ctx, c, namespace, "legacy", check.LegacyCertificateSecretName, check.LegacyCertificateKey, check.LegacyCertificateIsOperatorPEM)
		if !status.IsOK() {
			return withComponent(status)
		}
	}

	var problems []string
	if !validatedBy(legacyCerts[0], legacyCerts[1:], managedCAs) {
		if check.LegacyCAConfigMapName == "" {
			problems = append(problems, fmt.Sprintf("the selected legacy certificate is not trusted by the active managed CA bundle %s", check.ManagedCABundleName))
		} else {
			problems = append(problems, fmt.Sprintf(
				"the legacy certificate in Secret %s is not trusted by the active managed CA bundle %s; add the CA that signed it to %s, for example %s",
				check.LegacyCertificateSecretName, check.ManagedCABundleName, check.AdditionalTrustedCAsField, caRefSnippetForKey(check.LegacyCAConfigMapName, check.LegacyCAConfigMapKey)))
		}
	}
	// The legacy CA file is the reference the refusal names. A detected migration that cannot
	// read it must not pass, even when the active bundle already trusts the legacy leaf.
	if check.LegacyCAConfigMapName != "" {
		if _, status := readMigrationCAConfigMap(ctx, c, namespace, check.LegacyCAConfigMapName, check.LegacyCAConfigMapKey); !status.IsOK() {
			return withComponent(status)
		}
	}

	if len(problems) > 0 {
		return workflow.Failed(xerrors.Errorf(
			"%s cannot migrate to operator-managed certificates: %s",
			check.Component, strings.Join(problems, "; ")))
	}
	return workflow.OK()
}

// readActiveTrustedCAs returns the certificates of the bundle the workload currently selects. The
// ConfigMap can also retain the previous bundle, which the agent may still read during a rotation;
// that bundle does not count as configured trust for this migration.
func readActiveTrustedCAs(ctx context.Context, c client.Reader, namespace, name string) ([]*x509.Certificate, workflow.Status) {
	cm, status := readMigrationObject(ctx, c, kube.ObjectKey(namespace, name), &corev1.ConfigMap{}, "the active managed CA bundle %s does not exist", name)
	if !status.IsOK() {
		return nil, status
	}

	hash := strings.TrimSpace(cm.Data[util.LatestHashSecretKey])
	if hash == "" {
		return nil, workflow.Failed(xerrors.Errorf("the managed CA bundle %s has no active bundle hash", name))
	}
	return certificatesFromValue(cm.Data[hash], "the managed CA bundle %s has no active certificates under %s", name, hash)
}

// readMigrationCertificate reads the certificate file the workload uses today. The whole PEM is
// returned so the intermediates that follow the leaf can be used to connect it to a root.
// ReadIssuedCertificate reads the leaf a cert-manager Secret holds, plus any intermediates after it.
func ReadIssuedCertificate(ctx context.Context, c client.Reader, namespace, secretName string) ([]*x509.Certificate, workflow.Status) {
	return readMigrationCertificate(ctx, c, namespace, "managed", secretName, nil, false)
}

// ReadLegacyCA reads the CA file the deployment trusted before the migration.
func ReadLegacyCA(ctx context.Context, c client.Reader, namespace, name, key string) ([]*x509.Certificate, workflow.Status) {
	return readMigrationCAConfigMap(ctx, c, namespace, name, key)
}

// CertificateValidatedBy reports whether the managed trust anchors validate the certificate.
func CertificateValidatedBy(cert *x509.Certificate, intermediates, cas []*x509.Certificate) bool {
	return validatedBy(cert, intermediates, cas)
}

func readMigrationCertificate(ctx context.Context, c client.Reader, namespace, role, secretName string, selectKey func(corev1.Secret) string, operatorPEM bool) ([]*x509.Certificate, workflow.Status) {
	if secretName == "" {
		return nil, workflow.Failed(xerrors.Errorf("the %s certificate Secret is not configured", role))
	}
	s, status := readMigrationObject(ctx, c, kube.ObjectKey(namespace, secretName), &corev1.Secret{}, "the %s certificate Secret %s does not exist", role, secretName)
	if !status.IsOK() {
		return nil, status
	}

	key := corev1.TLSCertKey
	if operatorPEM {
		key = currentHashOfPEMSecret(secrets.DataToStringData(s.Data))
		if key == "" {
			return nil, workflow.Failed(xerrors.Errorf("the %s certificate Secret %s names no current certificate file", role, secretName))
		}
	} else if selectKey != nil {
		key = selectKey(*s)
	}
	value := string(s.Data[key])
	if !operatorPEM && strings.TrimSpace(value) == "" {
		return nil, workflow.Failed(xerrors.Errorf("the %s certificate Secret %s has no certificate under %s", role, secretName, key))
	}
	return certificatesFromValue(value, "the %s certificate Secret %s has no valid certificate under %s", role, secretName, key)
}

// readMigrationCAConfigMap reads the CA file the deployment trusts today.
func readMigrationCAConfigMap(ctx context.Context, c client.Reader, namespace, name, key string) ([]*x509.Certificate, workflow.Status) {
	if name == "" {
		return nil, workflow.Failed(xerrors.New("the legacy CA ConfigMap is not configured"))
	}
	cm, status := readMigrationObject(ctx, c, kube.ObjectKey(namespace, name), &corev1.ConfigMap{}, "the legacy CA ConfigMap %s does not exist", name)
	if !status.IsOK() {
		return nil, status
	}
	return certificatesFromValue(cm.Data[key], "the legacy CA ConfigMap %s has no valid certificate under %s", name, key)
}

func readMigrationObject[T client.Object](ctx context.Context, c client.Reader, key client.ObjectKey, obj T, missingFormat string, args ...any) (T, workflow.Status) {
	err := c.Get(ctx, key, obj)
	if apiErrors.IsNotFound(err) {
		return obj, workflow.Failed(xerrors.Errorf(missingFormat, args...))
	}
	if err != nil {
		return obj, workflow.Failed(err)
	}
	return obj, workflow.OK()
}

func certificatesFromValue(value, emptyFormat string, args ...any) ([]*x509.Certificate, workflow.Status) {
	certs := certificatesIn(value)
	if len(certs) == 0 {
		return nil, workflow.Failed(xerrors.Errorf(emptyFormat, args...))
	}
	return certs, workflow.OK()
}

// caRefSnippetForKey renders an additionalTrustedCAs entry that references the CA file the
// component actually uses. Ops Manager uses mms-ca.crt; AppDB and MongoDB use ca-pem.
func caRefSnippetForKey(configMapName, key string) string {
	return fmt.Sprintf(`{"name": %q, "kind": "ConfigMap", "key": %q}`, configMapName, key)
}
