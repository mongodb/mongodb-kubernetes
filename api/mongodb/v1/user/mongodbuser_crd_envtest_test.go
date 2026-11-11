package user_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbusers.yaml")))
}

// newUser builds the resource as unstructured: a typed object always serialises the required strings.
func newUser(t *testing.T, mutate func(map[string]any)) *unstructured.Unstructured {
	t.Helper()
	spec := map[string]any{
		"username":             "test-user",
		"db":                   "admin",
		"passwordSecretKeyRef": map[string]any{"name": "user-password", "key": "password"},
		"mongodbResourceRef":   map[string]any{"name": "my-replica-set"},
		"roles": []any{
			map[string]any{"name": "clusterAdmin", "db": "admin"},
		},
	}
	if mutate != nil {
		mutate(spec)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mongodb.com/v1",
		"kind":       "MongoDBUser",
		"spec":       spec,
	}}
	obj.SetGenerateName("test-user-")
	obj.SetNamespace("default")
	return obj
}

func firstRole(spec map[string]any) map[string]any {
	return spec["roles"].([]any)[0].(map[string]any)
}

func TestMongoDBUserCRDValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		mutate        func(map[string]any)
		errorContains string
	}{
		{
			name: "valid user is accepted",
		},
		{
			name:          "missing username is rejected",
			mutate:        func(spec map[string]any) { delete(spec, "username") },
			errorContains: "spec.username: Required value",
		},
		{
			name:          "missing db is rejected",
			mutate:        func(spec map[string]any) { delete(spec, "db") },
			errorContains: "spec.db: Required value",
		},
		{
			name:          "missing role name is rejected",
			mutate:        func(spec map[string]any) { delete(firstRole(spec), "name") },
			errorContains: "spec.roles[0].name: Required value",
		},
		{
			name:          "missing role db is rejected",
			mutate:        func(spec map[string]any) { delete(firstRole(spec), "db") },
			errorContains: "spec.roles[0].db: Required value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := newUser(t, tc.mutate)
			err := k8sClient.Create(ctx, obj)
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
