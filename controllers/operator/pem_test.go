package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
)

func TestGetPEMHashIsDeterministic(t *testing.T) {
	pemCollection := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname1": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
			"myhostname2": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}
	firstHash, err := pemCollection.GetHash()
	assert.NoError(t, err)

	// modify the PEM collection and check the hash is different
	pemCollection.PemFiles["myhostname3"] = pem.File{
		PrivateKey:  "thirdey",
		Certificate: "thirdcert",
	}
	secondHash, err := pemCollection.GetHash()
	assert.NoError(t, err)
	assert.NotEqual(t, firstHash, secondHash)

	// revert the changes to the PEM collection and check the hash is the same
	delete(pemCollection.PemFiles, "myhostname3")
	thirdHash, err := pemCollection.GetHash()
	assert.NoError(t, err)
	assert.Equal(t, firstHash, thirdHash)
}

func TestMergeEntryOverwritesOldSecret(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "mycert", p.PemFiles["myhostname"].Certificate)
}

func TestMergeEntryOnlyCertificate(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myhostname": {
				PrivateKey: "mykey",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "mykey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.PemFiles["myhostname"].Certificate)
}

func TestMergeEntryPreservesOldSecret(t *testing.T) {
	p := pem.Collection{
		PemFiles: map[string]pem.File{
			"myexistinghostname": {
				PrivateKey:  "mykey",
				Certificate: "mycert",
			},
		},
	}

	secretData := pem.File{
		PrivateKey:  "oldkey",
		Certificate: "oldcert",
	}

	p.MergeEntry("myhostname", secretData)
	assert.Equal(t, "oldkey", p.PemFiles["myhostname"].PrivateKey)
	assert.Equal(t, "oldcert", p.PemFiles["myhostname"].Certificate)
	assert.Equal(t, "mykey", p.PemFiles["myexistinghostname"].PrivateKey)
	assert.Equal(t, "mycert", p.PemFiles["myexistinghostname"].Certificate)
}

type mockSecretGetter struct {
	secret *corev1.Secret
}

func (m mockSecretGetter) GetSecret(_ context.Context, _ client.ObjectKey) (corev1.Secret, error) {
	if m.secret == nil {
		return corev1.Secret{}, xerrors.Errorf("not found")
	}
	return *m.secret, nil
}

func (m mockSecretGetter) CreateSecret(_ context.Context, _ corev1.Secret) error {
	return nil
}

func (m mockSecretGetter) UpdateSecret(_ context.Context, _ corev1.Secret) error {
	return nil
}

func (m mockSecretGetter) DeleteSecret(_ context.Context, _ types.NamespacedName) error {
	return nil
}

func TestReadPemHashFromSecret(t *testing.T) {
	ctx := context.Background()
	name := "res-name"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-cert", Namespace: mock.TestNamespace},
		Data:       map[string][]byte{"hello": []byte("world")},
		Type:       corev1.SecretTypeTLS,
	}

	assert.Empty(t, pem.ReadHashFromSecret(ctx, secrets.SecretClient{
		VaultClient: nil,
		KubeClient:  mockSecretGetter{},
	}, mock.TestNamespace, name, "", zap.S()), "secret does not exist so pem hash should be empty")

	hash := pem.ReadHashFromSecret(ctx, secrets.SecretClient{
		VaultClient: nil,
		KubeClient:  mockSecretGetter{secret: secret},
	}, mock.TestNamespace, name, "", zap.S())

	hash2 := pem.ReadHashFromSecret(ctx, secrets.SecretClient{
		VaultClient: nil,
		KubeClient:  mockSecretGetter{secret: secret},
	}, mock.TestNamespace, name, "", zap.S())

	assert.NotEmpty(t, hash, "pem hash should be read from the secret")
	assert.Equal(t, hash, hash2, "hash creation should be idempotent")
}

func TestReadPemHashFromSecretOpaqueType(t *testing.T) {
	ctx := context.Background()

	name := "res-name"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-cert", Namespace: mock.TestNamespace},
		Data:       map[string][]byte{"hello": []byte("world")},
		Type:       corev1.SecretTypeOpaque,
	}

	assert.Empty(t, pem.ReadHashFromSecret(ctx, secrets.SecretClient{
		VaultClient: nil,
		KubeClient:  mockSecretGetter{secret: secret},
	}, mock.TestNamespace, name, "", zap.S()), "if secret type is not TLS the empty string should be returned")
}
