package tls

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
)

const (
	CAMountPath             = "/var/lib/tls/ca/"
	OperatorSecretMountPath = "/var/lib/tls/server/" //nolint

	tlsSecretCertName = "tls.crt"
	tlsSecretKeyName  = "tls.key"
	tlsSecretPemName  = "tls.pem"
)

type TLSConfigurableResource interface {
	metav1.Object
	TLSSecretNamespacedName() types.NamespacedName
	TLSOperatorSecretNamespacedName() types.NamespacedName
}

// ensureTLSSecret will create or update the operator-managed Secret containing
// the concatenated certificate and key from the user-provided Secret.
// Returns the file name of the concatenated certificate and key
func EnsureTLSSecret(ctx context.Context, getUpdateCreator secret.GetUpdateCreator, resource TLSConfigurableResource) (string, error) {
	certKey, err := getPemOrConcatenatedCrtAndKey(ctx, getUpdateCreator, resource.TLSSecretNamespacedName())
	if err != nil {
		return "", err
	}
	// Calculate file name from certificate and key
	fileName := OperatorSecretFileName(certKey)

	operatorSecret := secret.Builder().
		SetName(resource.TLSOperatorSecretNamespacedName().Name).
		SetNamespace(resource.TLSOperatorSecretNamespacedName().Namespace).
		SetField(fileName, certKey).
		SetOwnerReferences(resource.GetOwnerReferences()).
		Build()

	return fileName, secret.CreateOrUpdate(ctx, getUpdateCreator, operatorSecret)
}

// getCertAndKey will fetch the certificate and key from the user-provided Secret.
func getCertAndKey(ctx context.Context, getter secret.Getter, secretName types.NamespacedName) string {
	cert, err := secret.ReadKey(ctx, getter, tlsSecretCertName, secretName)
	if err != nil {
		return ""
	}

	key, err := secret.ReadKey(ctx, getter, tlsSecretKeyName, secretName)
	if err != nil {
		return ""
	}

	return combineCertificateAndKey(cert, key)
}

// getPem will fetch the pem from the user-provided secret
func getPem(ctx context.Context, getter secret.Getter, secretName types.NamespacedName) string {
	pem, err := secret.ReadKey(ctx, getter, tlsSecretPemName, secretName)
	if err != nil {
		return ""
	}
	return pem
}

func combineCertificateAndKey(cert, key string) string {
	trimmedCert := strings.TrimRight(cert, "\n")
	trimmedKey := strings.TrimRight(key, "\n")
	return fmt.Sprintf("%s\n%s", trimmedCert, trimmedKey)
}

// getPemOrConcatenatedCrtAndKey will get the final PEM to write to the secret.
// This is either the tls.pem entry in the given secret, or the concatenation
// of tls.crt and tls.key
// It performs a basic validation on the entries.
func getPemOrConcatenatedCrtAndKey(ctx context.Context, getter secret.Getter, secretName types.NamespacedName) (string, error) {
	certKey := getCertAndKey(ctx, getter, secretName)
	pem := getPem(ctx, getter, secretName)
	if certKey == "" && pem == "" {
		return "", fmt.Errorf(`neither "%s" nor the pair "%s"/"%s" were present in the TLS secret`, tlsSecretPemName, tlsSecretCertName, tlsSecretKeyName)
	}
	if certKey == "" {
		return pem, nil
	}
	if pem == "" {
		return certKey, nil
	}
	if certKey != pem {
		return "", fmt.Errorf(`if all of "%s", "%s" and "%s" are present in the secret, the entry for "%s" must be equal to the concatenation of "%s" with "%s"`, tlsSecretCertName, tlsSecretKeyName, tlsSecretPemName, tlsSecretPemName, tlsSecretCertName, tlsSecretKeyName)
	}
	return certKey, nil
}

// OperatorSecretFileName calculates the file name to use for the mounted
// certificate-key file. The name is based on the hash of the combined cert and key.
// If the certificate or key changes, the file path changes as well which will trigger
// the agent to perform a restart.
// The user-provided secret is being watched and will trigger a reconciliation
// on changes. This enables the operator to automatically handle cert rotations.
func OperatorSecretFileName(certKey string) string {
	hash := sha256.Sum256([]byte(certKey))
	return fmt.Sprintf("%x.pem", hash)
}
