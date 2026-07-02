package operatorconfig

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const testNamespace = "test-ns"

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = operatorv1.AddToScheme(s)
	return s
}

func TestLoad_AbsentCR(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

	require.NoError(t, err)
	assert.Empty(t, cfg.ResourceVersion)
	assert.Equal(t, operatorv1.ArchitectureNonStatic, cfg.Spec.DefaultArchitecture)
	assert.Equal(t, 1, cfg.Spec.MaxConcurrentReconciles)
	assert.Equal(t, operatorv1.AllWatchedResources, cfg.Spec.WatchedResources)
}

func TestLoad_PresentCR(t *testing.T) {
	cr := &operatorv1.OperatorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      util.DefaultOperatorConfigName,
			Namespace: testNamespace,
		},
		Spec: operatorv1.OperatorConfigSpec{
			MaxConcurrentReconciles: 4,
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

	cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

	require.NoError(t, err)
	assert.Equal(t, 4, cfg.Spec.MaxConcurrentReconciles)
	// DefaultArchitecture was omitted in the CR; withDefaults fills it in
	assert.Equal(t, operatorv1.ArchitectureNonStatic, cfg.Spec.DefaultArchitecture)
	// WatchedResources was omitted in the CR; withDefaults fills in all known CRDs
	assert.Equal(t, operatorv1.AllWatchedResources, cfg.Spec.WatchedResources)
	assert.NotEmpty(t, cfg.ResourceVersion)
}

func TestLoad_WatchedResourcesSubsetPreserved(t *testing.T) {
	subset := []operatorv1.WatchedResource{
		operatorv1.WatchedResourceMongoDB,
		operatorv1.WatchedResourceMongoDBUsers,
	}
	cr := &operatorv1.OperatorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      util.DefaultOperatorConfigName,
			Namespace: testNamespace,
		},
		Spec: operatorv1.OperatorConfigSpec{
			WatchedResources: subset,
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

	cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

	require.NoError(t, err)
	assert.Equal(t, subset, cfg.Spec.WatchedResources)
}

func TestLoad_ClientError(t *testing.T) {
	c := &failingClient{fake.NewClientBuilder().WithScheme(testScheme()).Build()}

	_, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

	require.Error(t, err)
}

// failingClient wraps a fake client and forces Get to return a non-NotFound error.
type failingClient struct {
	client.Client
}

func (f *failingClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return fmt.Errorf("simulated API server failure")
}
