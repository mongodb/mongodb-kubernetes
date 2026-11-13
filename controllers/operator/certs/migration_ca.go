package certs

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
)

// VerifyMigrationTrustsLegacyCA checks that a migration to operator-managed certificates keeps the CA
// the deployment trusts today, and fails with the value to set when it would not.
//
// A deployment trusts the CA of its certificates: the members validate each other's certificates
// against it, and the agent validates the certificate the members serve. The certificates in use are
// signed by the CA the user configured, while the managed ones are signed by the issuer in the
// managedCertificate block, which can be a different CA. During the migration the pods restart one at a
// time, so for a while the members that have moved to the new CA and the ones still presenting the
// certificates signed by the old CA talk to each other, and each side has to trust the other's CA.
// Without the old CA in the trust bundle the members reject each other until every pod has restarted,
// which is the outage the migration is meant to avoid.
//
// The user keeps the old CA trusted by adding it to the category's additionalTrustedCAs. This check
// tells them so, rather than letting the deployment break while it restarts.
func VerifyMigrationTrustsLegacyCA(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, lastAchieved *mdbv1.Security, log *zap.SugaredLogger) workflow.Status {
	legacyCAName := legacyCAConfigMapName(lastAchieved)
	if legacyCAName == "" {
		// The deployment trusted no CA of its own, so there is nothing that has to stay trusted.
		return workflow.OK()
	}

	legacyCAs, status := readCACertificates(ctx, c, res.GetNamespace(), legacyCAName, tls.CAConfigMapKey)
	if !status.IsOK() {
		return status
	}
	if len(legacyCAs) == 0 {
		return workflow.OK()
	}

	// The members validate each other's member certificates against the server bundle, and their
	// clusterfiles against the client bundle, so the old CA has to be in both.
	bundles := []struct {
		name  string
		field string
	}{
		{ManagedServerCABundleConfigMapName(res.GetName()), "server"},
	}
	if res.GetSecurity().RequiresX509ClientCerts() {
		bundles = append(bundles, struct {
			name  string
			field string
		}{ManagedClientCABundleConfigMapName(res.GetName()), "client"})
	}

	var problems []string
	for _, bundle := range bundles {
		trusted, found, status := readTrustedCAs(ctx, c, res.GetNamespace(), bundle.name)
		if !status.IsOK() {
			return status
		}
		if !found {
			// The bundle is published with the managed certificates, which happens on a later reconcile
			// than the one that detects the migration. Nothing to compare yet.
			continue
		}
		for _, legacyCA := range legacyCAs {
			if !containsCertificate(trusted, legacyCA) {
				problems = append(problems, fmt.Sprintf(
					"the CA in ConfigMap %s is not in %s, so the members would reject the certificates signed by it while they restart. Add it to spec.security.managedCertificate.%s.additionalTrustedCAs as %s",
					legacyCAName, bundle.name, bundle.field, caRefSnippet(legacyCAName)))
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

// legacyCAConfigMapName returns the name of the ConfigMap the deployment trusts today, empty when it
// configured none.
func legacyCAConfigMapName(lastAchieved *mdbv1.Security) string {
	if lastAchieved == nil || lastAchieved.TLSConfig == nil {
		return ""
	}
	return lastAchieved.TLSConfig.CA
}

// readCACertificates reads the certificates a ConfigMap key holds. found is false when the ConfigMap or
// the key is not there.
func readCACertificates(ctx context.Context, c kubernetesClient.Client, namespace, name, key string) ([]*x509.Certificate, workflow.Status) {
	cm, err := c.GetConfigMap(ctx, kube.ObjectKey(namespace, name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, workflow.OK()
		}
		return nil, workflow.Failed(err)
	}
	return certificatesIn(cm.Data[key]), workflow.OK()
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

// containsCertificate reports whether the certificates hold the one given, by its encoded form.
func containsCertificate(certificates []*x509.Certificate, wanted *x509.Certificate) bool {
	for _, cert := range certificates {
		if bytes.Equal(cert.Raw, wanted.Raw) {
			return true
		}
	}
	return false
}

// caRefSnippet renders the additionalTrustedCAs entry that keeps a ConfigMap's CA trusted.
func caRefSnippet(configMapName string) string {
	return fmt.Sprintf(`{"name": %q, "kind": "ConfigMap", "key": %q}`, configMapName, tls.CAConfigMapKey)
}
