package om_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
)

// Compile-time check: AppDBSpec must satisfy project.Reader.
var _ project.Reader = &omv1.AppDBSpec{}

func TestAppDBSpec_ReaderMethods(t *testing.T) {
	spec := &omv1.AppDBSpec{
		Namespace:      "test-ns",
		OpsManagerName: "om-primary",
	}
	spec.Connection = &omv1.ConnectionSpec{
		SharedConnectionSpec: mdbv1.SharedConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-project-config"},
			},
		},
		Credentials: "my-credentials-secret",
	}

	assert.Equal(t, "om-primary-db", spec.GetName())
	assert.Equal(t, "my-project-config", spec.GetProjectConfigMapName())
	assert.Equal(t, "test-ns", spec.GetProjectConfigMapNamespace())
	assert.Equal(t, "my-credentials-secret", spec.GetCredentialsSecretName())
	assert.Equal(t, "test-ns", spec.GetCredentialsSecretNamespace())
}

func TestAppDBSpec_ReaderMethods_NilConnection(t *testing.T) {
	spec := &omv1.AppDBSpec{
		Namespace:      "test-ns",
		OpsManagerName: "om-primary",
	}
	// Connection is nil — all Reader methods should return "" safely
	assert.Equal(t, "om-primary-db", spec.GetName())
	assert.Equal(t, "", spec.GetProjectConfigMapName())
	assert.Equal(t, "test-ns", spec.GetProjectConfigMapNamespace())
	assert.Equal(t, "", spec.GetCredentialsSecretName())
	assert.Equal(t, "test-ns", spec.GetCredentialsSecretNamespace())
}
