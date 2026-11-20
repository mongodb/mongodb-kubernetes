package operatoridentity

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

func TestLoad(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("present ConfigMap returns the cluster name", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(identityConfigMap("cluster-a")).Build()
		identity, err := Load(context.Background(), c, testNamespace)
		require.NoError(t, err)
		assert.Equal(t, "cluster-a", identity)
	})

	t.Run("missing ConfigMap means no identity", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		identity, err := Load(context.Background(), c, testNamespace)
		require.NoError(t, err)
		assert.Equal(t, "", identity)
	})

	t.Run("missing or empty clusterName key is an error", func(t *testing.T) {
		for _, cm := range []*corev1.ConfigMap{
			identityConfigMap(""),
			{
				ObjectMeta: metav1.ObjectMeta{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: testNamespace},
				Data:       map[string]string{"other": "x"},
			},
		} {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
			identity, err := Load(context.Background(), c, testNamespace)
			require.Error(t, err)
			assert.Equal(t, "", identity)
		}
	})

	t.Run("API errors are returned with no identity", func(t *testing.T) {
		expectedErr := errors.New("api unavailable")
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return expectedErr
			},
		}).Build()
		identity, err := Load(context.Background(), c, testNamespace)
		assert.ErrorIs(t, err, expectedErr)
		assert.Equal(t, "", identity)
	})
}
