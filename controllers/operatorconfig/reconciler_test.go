package operatorconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

const (
	operatorNamespace = "operator-ns"
	configName        = "operator-config"
	loadedRV          = "1000"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = operatorv1.AddToScheme(s)
	return s
}

func configKey() types.NamespacedName {
	return types.NamespacedName{Name: configName, Namespace: operatorNamespace}
}

// newReconciler builds a Reconciler and returns a channel that receives a value when cancel is called.
func newReconciler(c *fake.ClientBuilder, rv string) (*Reconciler, <-chan struct{}) {
	cancelled := make(chan struct{}, 1)
	r := &Reconciler{
		client:                c.Build(),
		cancel:                func() { cancelled <- struct{}{} },
		loadedResourceVersion: rv,
	}
	return r, cancelled
}

func TestReconcile(t *testing.T) {
	for _, tc := range []struct {
		name         string
		crRV         string
		expectCancel bool
	}{
		{
			name:         "unchanged ResourceVersion does not trigger restart",
			crRV:         loadedRV,
			expectCancel: false,
		},
		{
			name:         "changed ResourceVersion triggers restart",
			crRV:         "1001",
			expectCancel: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &operatorv1.OperatorConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:            configName,
					Namespace:       operatorNamespace,
					ResourceVersion: tc.crRV,
				},
			}
			r, cancelled := newReconciler(fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr), loadedRV)

			_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: configKey()})

			require.NoError(t, err)
			if tc.expectCancel {
				assert.Len(t, cancelled, 1)
			} else {
				assert.Empty(t, cancelled)
			}
		})
	}
}

func TestReconcile_Deletion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		loadedRV     string
		expectCancel bool
	}{
		{
			name:         "deletion after CR existed at startup triggers restart",
			loadedRV:     loadedRV,
			expectCancel: true,
		},
		{
			name:         "deletion when no CR existed at startup is a no-op",
			loadedRV:     "",
			expectCancel: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No CR in the fake client — simulates a deleted or never-existing object.
			r, cancelled := newReconciler(fake.NewClientBuilder().WithScheme(testScheme()), tc.loadedRV)

			_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: configKey()})

			require.NoError(t, err)
			if tc.expectCancel {
				assert.Len(t, cancelled, 1)
			} else {
				assert.Empty(t, cancelled)
			}
		})
	}
}
