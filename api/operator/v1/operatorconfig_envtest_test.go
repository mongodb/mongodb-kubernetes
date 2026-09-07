package v1_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestOperatorConfigValidation proves that the CEL validation rules defined on the
// OperatorConfig CRD (the telemetry duration minimums) are enforced by a real
// Kubernetes API server (booted locally via envtest).
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
		{
			name: "valid telemetry durations are accepted",
			spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Collection: &operatorv1.TelemetryCollectionConfig{
						Frequency:   &metav1.Duration{Duration: time.Minute},
						KubeTimeout: &metav1.Duration{Duration: time.Second},
					},
					Send: &operatorv1.TelemetrySendConfig{
						Frequency: &metav1.Duration{Duration: time.Hour},
					},
				},
			},
		},
		{
			name: "telemetry.collection.frequency must be at least 1m",
			spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Collection: &operatorv1.TelemetryCollectionConfig{
						Frequency: &metav1.Duration{Duration: 30 * time.Second},
					},
				},
			},
			errorContains: "frequency must be at least 1m",
		},
		{
			name: "telemetry.collection.kubeTimeout must be at least 1s",
			spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Collection: &operatorv1.TelemetryCollectionConfig{
						KubeTimeout: &metav1.Duration{Duration: 500 * time.Millisecond},
					},
				},
			},
			errorContains: "kubeTimeout must be at least 1s",
		},
		{
			name: "telemetry.send.frequency must be at least 1h",
			spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Send: &operatorv1.TelemetrySendConfig{
						Frequency: &metav1.Duration{Duration: 30 * time.Minute},
					},
				},
			},
			errorContains: "frequency must be at least 1h",
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
