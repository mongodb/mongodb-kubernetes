package certs

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

type certDestination string

const (
	OperatorGeneratedCertSuffix = "-pem"

	Unused     = "unused"
	Database   = "database"
	OpsManager = "opsmanager"
	AppDB      = "appdb"
)

// CreateOrUpdatePEMSecretWithPreviousCert creates a PEM secret from the original secretName.
// Additionally, this method verifies if there already exists a PEM secret, and it will merge them to be able to keep the newest and the previous certificate.
func CreateOrUpdatePEMSecretWithPreviousCert(ctx context.Context, secretClient secrets.SecretClient, secretNamespacedName types.NamespacedName, certificateKey string, certificateValue string, ownerReferences []metav1.OwnerReference, podType certDestination) error {
	path, err := getVaultBasePath(secretClient, podType)
	if err != nil {
		return err
	}

	secretData, err := updateSecretDataWithPreviousCert(ctx, secretClient, getOperatorGeneratedSecret(secretNamespacedName), certificateKey, certificateValue, path)
	if err != nil {
		return err
	}

	return CreateOrUpdatePEMSecret(ctx, secretClient, secretNamespacedName, secretData, ownerReferences, podType)
}

// CreateOrUpdatePEMSecret creates a PEM secret from the original secretName.
func CreateOrUpdatePEMSecret(ctx context.Context, secretClient secrets.SecretClient, secretNamespacedName types.NamespacedName, secretData map[string]string, ownerReferences []metav1.OwnerReference, podType certDestination) error {
	operatorGeneratedSecret := getOperatorGeneratedSecret(secretNamespacedName)

	path, err := getVaultBasePath(secretClient, podType)
	if err != nil {
		return err
	}

	secretBuilder := secret.Builder().
		SetName(operatorGeneratedSecret.Name).
		SetNamespace(operatorGeneratedSecret.Namespace).
		SetStringMapToData(secretData).
		SetOwnerReferences(ownerReferences)

	return secretClient.PutSecretIfChanged(ctx, secretBuilder.Build(), path)
}

// updateSecretDataWithPreviousCert receives the new TLS certificate and returns the data for the new concatenated Pem Secret
// This method read the existing -pem secret and creates the secret data such that it keeps the previous TLS certificate
func updateSecretDataWithPreviousCert(ctx context.Context, secretClient secrets.SecretClient, operatorGeneratedSecret types.NamespacedName, certificateKey string, certificateValue string, basePath string) (map[string]string, error) {
	newData := map[string]string{certificateKey: certificateValue}
	newLatestHash := certificateKey

	newData[util.LatestHashSecretKey] = newLatestHash

	existingSecretData, err := secretClient.ReadSecret(ctx, operatorGeneratedSecret, basePath)
	if secrets.SecretNotExist(err) {
		// Case: creating the PEM secret the first time (example: enabling TLS)
		return newData, nil
	} else if err != nil {
		return nil, err
	}

	if oldLatestHash, ok := existingSecretData[util.LatestHashSecretKey]; ok {
		if oldLatestHash == newLatestHash {
			// Case: no new changes, the pem secrets have the annotations, no rotation happened
			newData = existingSecretData
		} else {
			// Case: the pem secrets have the annotations, and a rotation happened
			newData[util.PreviousHashSecretKey] = oldLatestHash
			newData[oldLatestHash] = existingSecretData[oldLatestHash]
		}
	} else if len(existingSecretData) == 1 {
		// Case: the operator is upgraded to 1.29, the pem secrets don't have the annotations
		for hash, cert := range existingSecretData {
			if hash != newLatestHash {
				// Case: the operator is upgraded to 1.29, the pem secrets don't have the annotations, and a certificate rotation happened
				newData[hash] = cert
				newData[util.PreviousHashSecretKey] = hash
			}
		}
	}

	return newData, nil
}

// getVaultBasePath returns the path to secrets in the vault
func getVaultBasePath(secretClient secrets.SecretClient, podType certDestination) (string, error) {
	var path string
	if vault.IsVaultSecretBackend() && podType != Unused {
		switch podType {
		case Database:
			path = secretClient.VaultClient.DatabaseSecretPath()
		case OpsManager:
			path = secretClient.VaultClient.OpsManagerSecretPath()
		case AppDB:
			path = secretClient.VaultClient.AppDBSecretPath()
		default:
			return "", xerrors.Errorf("unexpected pod type got: %s", podType)
		}
	}
	return path, nil
}

// getOperatorGeneratedSecret returns the namespaced name of the PEM secret the operator creates
func getOperatorGeneratedSecret(secretNamespacedName types.NamespacedName) types.NamespacedName {
	operatorGeneratedSecret := secretNamespacedName
	operatorGeneratedSecret.Name = fmt.Sprintf("%s%s", secretNamespacedName.Name, OperatorGeneratedCertSuffix)
	return operatorGeneratedSecret
}

// VerifyTLSSecretForStatefulSet verifies a `Secret`'s `StringData` is a valid
// certificate, considering the amount of members for a resource named on
// `opts`.
func VerifyTLSSecretForStatefulSet(secretData map[string][]byte, opts Options) (string, error) {
	crt, key := secretData["tls.crt"], secretData["tls.key"]

	// add a line break to the end of certificate when performing concatenation
	crtString := string(crt)
	if !strings.HasSuffix(crtString, "\n") {
		crtString = crtString + "\n"
	}
	crt = []byte(crtString)

	data := append(crt, key...)

	var additionalDomains []string
	if len(opts.horizons) > 0 && len(opts.horizons) < opts.Replicas {
		return "", xerrors.Errorf("less horizon configs than number for replicas this reconcile. Please make sure that "+
			"enough horizon configs are configured as members are until the scale down has finished. "+
			"Current number of replicas for this reconciliation: %d, number of horizons: %d", opts.Replicas, len(opts.horizons))
	}
	for i := range getPodNames(opts) {
		additionalDomains = append(additionalDomains, GetAdditionalCertDomainsForMember(opts, i)...)
	}
	if opts.ExternalDomain != nil {
		additionalDomains = append(additionalDomains, "*."+*opts.ExternalDomain)
	}

	if err := validatePemData(data, additionalDomains); err != nil {
		return "", err
	}
	return string(data), nil
}

// VerifyAndEnsureCertificatesForStatefulSet ensures that the provided certificates are correct.
// If the secret is of type kubernetes.io/tls, it creates a new secret containing the concatenation fo the tls.crt and tls.key fields
func VerifyAndEnsureCertificatesForStatefulSet(ctx context.Context, secretReadClient, secretWriteClient secrets.SecretClient, secretName string, opts Options, log *zap.SugaredLogger) error {
	var err error
	var secretData map[string][]byte
	var s corev1.Secret
	var databaseSecretPath string

	if vault.IsVaultSecretBackend() {
		databaseSecretPath = secretReadClient.VaultClient.DatabaseSecretPath()
		secretData, err = secretReadClient.VaultClient.ReadSecretBytes(fmt.Sprintf("%s/%s/%s", databaseSecretPath, opts.Namespace, secretName))
		if err != nil {
			return err
		}

	} else {
		s, err = secretReadClient.KubeClient.GetSecret(ctx, kube.ObjectKey(opts.Namespace, secretName))
		if err != nil {
			return err
		}

		// SecretTypeTLS is kubernetes.io/tls
		// This is the standard way in K8S to have secrets that hold TLS certs
		// And it is the one generated by cert manager
		// These type of secrets contain tls.crt and tls.key entries
		if s.Type != corev1.SecretTypeTLS {
			return xerrors.Errorf("The secret object '%s' is not of type kubernetes.io/tls: %s", secretName, s.Type)
		}
		secretData = s.Data
	}

	data, err := VerifyTLSSecretForStatefulSet(secretData, opts)
	if err != nil {
		return err
	}

	secretHash := enterprisepem.ReadHashFromSecret(ctx, secretReadClient, opts.Namespace, secretName, databaseSecretPath, log)
	return CreateOrUpdatePEMSecretWithPreviousCert(ctx, secretWriteClient, kube.ObjectKey(opts.Namespace, secretName), secretHash, data, opts.OwnerReference, Database)
}

// getPodNames returns the pod names based on the Cert Options provided.
func getPodNames(opts Options) []string {
	_, podNames := dns.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas, nil)
	return podNames
}

func GetDNSNames(opts Options) (hostnames, podNames []string) {
	return dns.GetDNSNames(opts.ResourceName, opts.ServiceName, opts.Namespace, opts.ClusterDomain, opts.Replicas, nil)
}

// GetAdditionalCertDomainsForMember gets any additional domains that the
// certificate for the given member of the stateful set should be signed for.
func GetAdditionalCertDomainsForMember(opts Options, member int) (hostnames []string) {
	_, podNames := GetDNSNames(opts)
	for _, certDomain := range opts.additionalCertificateDomains {
		hostnames = append(hostnames, podNames[member]+"."+certDomain)
	}
	if len(opts.horizons) > 0 {
		// at this point len(ss.ReplicaSetHorizons) should be equal to the number
		// of members in the replica set
		for _, externalHost := range opts.horizons[member] {
			// need to use the URL struct directly instead of url.Parse as
			// Parse expects the URL to have a scheme.
			hostURL := url.URL{Host: externalHost}
			hostnames = append(hostnames, hostURL.Hostname())
		}
	}
	return hostnames
}

func validatePemData(data []byte, additionalDomains []string) error {
	pemFile := enterprisepem.NewFileFromData(data)
	if !pemFile.IsComplete() {
		return xerrors.Errorf("the certificate is not complete\n")
	}
	certs, err := pemFile.ParseCertificate()
	if err != nil {
		return xerrors.Errorf("can't parse certificate: %w\n", err)
	}

	var errs error
	// in case of using an intermediate certificate authority, the certificate
	// data might contain all the certificate chain excluding the root-ca (in case of cert-manager).
	// We need to iterate through the certificates in the chain and find the one at the bottom of the chain
	// containing the additionalDomains which we're validating for.
	for _, cert := range certs {
		var err error
		for _, domain := range additionalDomains {
			if !stringutil.CheckCertificateAddresses(cert.DNSNames, domain) {
				err = xerrors.Errorf("domain %s is not contained in the list of DNSNames %v\n", domain, cert.DNSNames)
				errs = multierror.Append(errs, err)
			}
		}
		if err == nil {
			return nil
		}
	}

	return errs
}

// ValidateCertificates verifies the Secret containing the certificates and the keys is valid.
func ValidateCertificates(ctx context.Context, secretGetter secret.Getter, name, namespace string, log *zap.SugaredLogger) error {
	validateCertificates := func() (string, bool) {
		byteData, err := secret.ReadByteData(ctx, secretGetter, kube.ObjectKey(namespace, name))
		if err == nil {
			// Validate that the secret contains the keys, if it contains the certs.
			for key, value := range byteData {
				if key == util.LatestHashSecretKey || key == util.PreviousHashSecretKey {
					continue
				}
				pemFile := enterprisepem.NewFileFromData(value)
				if !pemFile.IsValid() {
					return fmt.Sprintf("The Secret %s containing certificates is not valid. Entries must contain a certificate and a private key.", name), false
				}
			}
		}
		return "", true
	}

	// we immediately create the certificate in a prior call, thus we need to retry to account for races.
	if found, msg := util.DoAndRetry(validateCertificates, log, 10, 5); !found {
		return xerrors.Errorf(msg)
	}
	return nil
}

// VerifyAndEnsureClientCertificatesForAgentsAndTLSType ensures that agent certs are present and correct, and returns whether they are of the kubernetes.io/tls type.
// If the secret is of type kubernetes.io/tls, it creates a new secret containing the concatenation fo the tls.crt and tls.key fields
func VerifyAndEnsureClientCertificatesForAgentsAndTLSType(ctx context.Context, secretReadClient, secretWriteClient secrets.SecretClient, secret types.NamespacedName, log *zap.SugaredLogger) error {
	var secretData map[string][]byte
	var s corev1.Secret
	var err error

	if vault.IsVaultSecretBackend() {
		databaseSecretPath = secretReadClient.VaultClient.DatabaseSecretPath()
		secretData, err = secretReadClient.VaultClient.ReadSecretBytes(fmt.Sprintf("%s/%s/%s", secretReadClient.VaultClient.DatabaseSecretPath(), secret.Namespace, secret.Name))
		if err != nil {
			return err
		}
	} else {
		s, err = secretReadClient.KubeClient.GetSecret(ctx, secret)
		if err != nil {
			return err
		}

		if s.Type != corev1.SecretTypeTLS {
			return xerrors.Errorf("the secret object %q containing agent certificate must be of type kubernetes.io/tls. Got: %q", s.Name, s.Type)
		}
		secretData = s.Data
	}

	data, err := VerifyTLSSecretForStatefulSet(secretData, Options{Replicas: 0})
	if err != nil {
		return err
	}

	secretHash := enterprisepem.ReadHashFromSecret(ctx, secretReadClient, secret.Namespace, secret.Name, databaseSecretPath, log)
	return CreateOrUpdatePEMSecretWithPreviousCert(ctx, secretWriteClient, secret, secretHash, data, []metav1.OwnerReference{}, Database)
}

// EnsureSSLCertsForStatefulSet contains logic to ensure that all of the
// required SSL certs for a StatefulSet object exist.
func EnsureSSLCertsForStatefulSet(ctx context.Context, secretReadClient, secretWriteClient secrets.SecretClient, ms mdbv1.Security, opts Options, log *zap.SugaredLogger) workflow.Status {
	if !ms.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return workflow.OK()
	}

	secretName := opts.CertSecretName
	return ValidateSelfManagedSSLCertsForStatefulSet(ctx, secretReadClient, secretWriteClient, secretName, opts, log)
}

// EnsureTLSCertsForPrometheus creates a new Secret with a Certificate in
// PEM-format. Returns the hash for the certificate in order to be used in
// AutomationConfig.
//
// For Prometheus we *only accept* certificates of type `corev1.SecretTypeTLS`
// so they always need to be concatenated into PEM-format.
func EnsureTLSCertsForPrometheus(ctx context.Context, secretClient secrets.SecretClient, namespace string, prom *mdbcv1.Prometheus, podType certDestination, log *zap.SugaredLogger) (string, error) {
	if prom == nil || prom.TLSSecretRef.Name == "" {
		return "", nil
	}

	var secretData map[string][]byte
	var err error

	var secretPath string
	if vault.IsVaultSecretBackend() {
		// TODO: This is calculated twice, can this be done better?
		// This "calculation" is used in ReadHashFromSecret but calculated again in `CreateOrUpdatePEMSecretWithPreviousCert`
		if podType == Database {
			secretPath = secretClient.VaultClient.DatabaseSecretPath()
		} else if podType == AppDB {
			secretPath = secretClient.VaultClient.AppDBSecretPath()
		}

		secretData, err = secretClient.VaultClient.ReadSecretBytes(fmt.Sprintf("%s/%s/%s", secretPath, namespace, prom.TLSSecretRef.Name))
		if err != nil {
			return "", err
		}
	} else {
		s, err := secretClient.KubeClient.GetSecret(ctx, kube.ObjectKey(namespace, prom.TLSSecretRef.Name))
		if err != nil {
			return "", xerrors.Errorf("could not read Prometheus TLS certificate: %w", err)
		}

		if s.Type != corev1.SecretTypeTLS {
			return "", xerrors.Errorf("secret containing the Prometheus TLS certificate needs to be of type kubernetes.io/tls")
		}

		secretData = s.Data
	}

	// We only need VerifyTLSSecretForStatefulSet to return the concatenated
	// tls.key and tls.crt as Strings, but to not divert from the existing code,
	// I'm still calling it, but that can be definitely improved.
	//
	// Make VerifyTLSSecretForStatefulSet receive `s.Data` but only return if it
	// has been verified to be valid or not (boolean return).
	//
	// Use another function to concatenate tls.key and tls.crt into a `string`,
	// or make `CreateOrUpdatePEMSecretWithPreviousCert` able to receive a byte[] instead on its
	// `data` parameter.
	data, err := VerifyTLSSecretForStatefulSet(secretData, Options{Replicas: 0})
	if err != nil {
		return "", err
	}

	// ReadHashFromSecret will read the Secret once again from Kubernetes API,
	// we can improve this function by providing the Secret Data contents,
	// instead of `secretClient`.
	secretHash := enterprisepem.ReadHashFromSecret(ctx, secretClient, namespace, prom.TLSSecretRef.Name, secretPath, log)
	err = CreateOrUpdatePEMSecretWithPreviousCert(ctx, secretClient, kube.ObjectKey(namespace, prom.TLSSecretRef.Name), secretHash, data, []metav1.OwnerReference{}, podType)
	if err != nil {
		return "", xerrors.Errorf("error creating hashed Secret: %w", err)
	}

	return secretHash, nil
}

// ValidateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func ValidateSelfManagedSSLCertsForStatefulSet(ctx context.Context, secretReadClient, secretWriteClient secrets.SecretClient, secretName string, opts Options, log *zap.SugaredLogger) workflow.Status {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	err := VerifyAndEnsureCertificatesForStatefulSet(ctx, secretReadClient, secretWriteClient, secretName, opts, log)
	if err != nil {
		return workflow.Failed(xerrors.Errorf("The secret object '%s' does not contain all the valid certificates needed: %w", secretName, err))
	}

	secretName = fmt.Sprintf("%s-pem", secretName)

	if err := ValidateCertificates(ctx, secretReadClient.KubeClient, secretName, opts.Namespace, log); err != nil {
		return workflow.Failed(err)
	}

	return workflow.OK()
}
