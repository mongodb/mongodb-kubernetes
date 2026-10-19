package user_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbusers.yaml")))
}

func TestMongoDBUserClientCertificateCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newUser := func(spec user.MongoDBUserSpec) *user.MongoDBUser {
		return &user.MongoDBUser{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-user-cel-", Namespace: "default"},
			Spec:       spec,
		}
	}
	ref := user.MongoDBResourceRef{Name: "my-rs"}

	tests := []struct {
		name string
		spec user.MongoDBUserSpec
		// errorContains is the expected CEL message; empty means the create must succeed.
		errorContains string
	}{
		{
			name: "a SCRAM user with a username is accepted",
			spec: user.MongoDBUserSpec{Username: "alice", Database: "admin", MongoDBResourceRef: ref},
		},
		{
			name:          "a user without clientCertificate needs a username",
			spec:          user.MongoDBUserSpec{Database: "admin", MongoDBResourceRef: ref},
			errorContains: "spec.username is required unless spec.clientCertificate is set",
		},
		{
			name: "clientCertificate with no username is accepted",
			spec: user.MongoDBUserSpec{Database: "$external", MongoDBResourceRef: ref, ClientCertificate: &user.ClientCertificate{}},
		},
		{
			name: "clientCertificate with a username is accepted",
			spec: user.MongoDBUserSpec{Username: "CN=my-app,OU=default,O=my-rs-app-client", Database: "$external", MongoDBResourceRef: ref, ClientCertificate: &user.ClientCertificate{}},
		},
		{
			name:          "clientCertificate requires $external",
			spec:          user.MongoDBUserSpec{Database: "admin", MongoDBResourceRef: ref, ClientCertificate: &user.ClientCertificate{}},
			errorContains: "spec.clientCertificate requires spec.db to be $external",
		},
		{
			name: "clientCertificate and a password are mutually exclusive",
			spec: user.MongoDBUserSpec{
				Database: "$external", MongoDBResourceRef: ref, ClientCertificate: &user.ClientCertificate{},
				PasswordSecretKeyRef: user.SecretKeyRef{Name: "pw", Key: "password"},
			},
			errorContains: "spec.clientCertificate and spec.passwordSecretKeyRef are mutually exclusive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(ctx, newUser(tc.spec))
			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
