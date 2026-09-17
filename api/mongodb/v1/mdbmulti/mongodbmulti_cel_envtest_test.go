package mdbmulti_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mcdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbmulticluster.yaml")))
}

func newMongoDBMultiCluster(t *testing.T, role string) *mcdbmulti.MongoDBMultiCluster {
	t.Helper()
	return &mcdbmulti.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
		Spec: mcdbmulti.MongoDBMultiSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      "6.0.0",
				ResourceType: mdbv1.ReplicaSet,
				Role:         role,
				ConnectionSpec: mdbv1.ConnectionSpec{
					Credentials: "my-creds",
				},
			},
		},
	}
}

func TestMongoDBMultiClusterCELValidation_RoleNotSupported(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		role          string
		errorContains string
	}{
		{
			name: "empty role is accepted",
			role: "",
		},
		{
			name:          "role AppDB is rejected",
			role:          mdbv1.RoleAppDB,
			errorContains: "spec.role is not supported on MongoDBMultiCluster",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mrs := newMongoDBMultiCluster(t, tc.role)
			err := k8sClient.Create(ctx, mrs)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
