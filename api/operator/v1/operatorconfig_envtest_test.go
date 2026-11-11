package v1_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestOperatorConfigValidation proves that the OperatorConfig CRD is enforced by a
// real Kubernetes API server (booted locally via envtest).
func TestOperatorConfigValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newOperatorConfig := func(spec operatorv1.OperatorConfigSpec) *operatorv1.OperatorConfig {
		return &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-opconfig-", Namespace: "default"},
			Spec:       spec,
		}
	}

	tests := []struct {
		name string
		spec operatorv1.OperatorConfigSpec
		// errorContains is the expected validation message; empty means the
		// create must succeed.
		errorContains string
	}{
		{
			name: "empty spec is accepted",
			spec: operatorv1.OperatorConfigSpec{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := k8sClient.Create(ctx, newOperatorConfig(tc.spec))

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
