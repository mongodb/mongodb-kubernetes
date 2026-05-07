package secret

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReadAutomationConfigVersionFromSecret(t *testing.T) {
	const (
		namespace  = "test-ns"
		secretName = "test-automation-config"
	)

	secretWithData := func(data []byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      secretName,
			},
			Data: map[string][]byte{
				automationConfigKey: data,
			},
		}
	}

	tests := []struct {
		name            string
		secret          *corev1.Secret
		expectedVersion int64
		expectedErrMsg  string
	}{
		{
			name:            "secret does not exist",
			secret:          nil,
			expectedVersion: -1,
			expectedErrMsg:  "failed to read automation config secret test-ns/test-automation-config",
		},
		{
			name:            "secret data contains invalid json",
			secret:          secretWithData([]byte("not-valid-json")),
			expectedVersion: -1,
			expectedErrMsg:  "failed to unmarshal automation config cluster-config.json key from test-ns/test-automation-config secret",
		},
		{
			name:            "version field missing from automation config",
			secret:          secretWithData([]byte(`{"otherField": 42}`)),
			expectedVersion: -1,
			expectedErrMsg:  "version field is missing in the automation config cluster-config.json key from test-ns/test-automation-config secret",
		},
		{
			name:            "correct secret structure with version field",
			secret:          secretWithData([]byte(`{"version": 5}`)),
			expectedVersion: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var clientSet *fake.Clientset
			if tc.secret != nil {
				clientSet = fake.NewClientset(tc.secret)
			} else {
				clientSet = fake.NewClientset()
			}

			version, err := ReadAutomationConfigVersionFromSecret(context.Background(), namespace, clientSet, secretName)

			assert.Equal(t, tc.expectedVersion, version)
			if tc.expectedErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
