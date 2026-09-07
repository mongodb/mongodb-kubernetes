package v1_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMemberClusterValidation proves that the CEL validation rule defined on the
// MemberCluster CRD (credentialSecretRef.name must not be empty) is enforced by a
// real Kubernetes API server (booted locally via envtest).
func TestMemberClusterValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newMemberCluster := func(clusterName, secretName string) *operatorv1.MemberCluster {
		return &operatorv1.MemberCluster{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-mc-", Namespace: "default"},
			Spec: operatorv1.MemberClusterSpec{
				ClusterName:         clusterName,
				CredentialSecretRef: corev1.LocalObjectReference{Name: secretName},
			},
		}
	}

	tests := []struct {
		name        string
		clusterName string
		secretName  string
		// errorContains is the expected validation message; empty means the
		// create must succeed.
		errorContains string
	}{
		{
			name:        "valid spec is accepted",
			clusterName: "cluster-a",
			secretName:  "cluster-a-credentials",
		},
		{
			name:          "credentialSecretRef.name must not be empty",
			clusterName:   "cluster-a",
			secretName:    "",
			errorContains: "credentialSecretRef.name must not be empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(ctx, newMemberCluster(tc.clusterName, tc.secretName))

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
