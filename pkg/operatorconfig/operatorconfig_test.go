package operatorconfig

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	// MultiCluster is a pointer; withDefaults must materialise it and default the timeout
	require.NotNil(t, cfg.Spec.MultiCluster)
	assert.Equal(t, 10, cfg.Spec.MultiCluster.MemberClusterClientTimeout)
	assert.Equal(t, 5, cfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak)
	// AutomaticRecovery is a pointer; withDefaults must materialise it and default mode+delay
	require.NotNil(t, cfg.Spec.AutomaticRecovery)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.AutomaticRecovery.Mode)
	assert.Equal(t, 1200, cfg.Spec.AutomaticRecovery.Delay)
	// Proxy is a pointer; withDefaults must materialise it and default the policy
	require.NotNil(t, cfg.Spec.Proxy)
	assert.Equal(t, operatorv1.ProxyEnvPropagationPolicyNoPropagation, cfg.Spec.Proxy.EnvPropagationPolicy)
	// Telemetry is a pointer tree; withDefaults must materialise every block and default to Enabled
	// with the documented frequencies/timeout (opt-out model).
	require.NotNil(t, cfg.Spec.Telemetry)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Mode)
	require.NotNil(t, cfg.Spec.Telemetry.Collection)
	require.NotNil(t, cfg.Spec.Telemetry.Collection.Frequency)
	assert.Equal(t, time.Hour, cfg.Spec.Telemetry.Collection.Frequency.Duration)
	require.NotNil(t, cfg.Spec.Telemetry.Collection.KubeTimeout)
	assert.Equal(t, 5*time.Minute, cfg.Spec.Telemetry.Collection.KubeTimeout.Duration)
	require.NotNil(t, cfg.Spec.Telemetry.Collection.Clusters)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Collection.Clusters.Mode)
	require.NotNil(t, cfg.Spec.Telemetry.Collection.Deployments)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Collection.Deployments.Mode)
	require.NotNil(t, cfg.Spec.Telemetry.Collection.Operators)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Collection.Operators.Mode)
	require.NotNil(t, cfg.Spec.Telemetry.Send)
	assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Send.Mode)
	require.NotNil(t, cfg.Spec.Telemetry.Send.Frequency)
	assert.Equal(t, 168*time.Hour, cfg.Spec.Telemetry.Send.Frequency.Duration)
}

func TestLoad_Proxy(t *testing.T) {
	t.Run("omitted proxy block defaults to NoPropagation", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.Proxy)
		assert.Equal(t, operatorv1.ProxyEnvPropagationPolicyNoPropagation, cfg.Spec.Proxy.EnvPropagationPolicy)
	})

	t.Run("explicit Propagate is preserved", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				Proxy: &operatorv1.ProxyConfig{
					EnvPropagationPolicy: operatorv1.ProxyEnvPropagationPolicyPropagate,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.Proxy)
		assert.Equal(t, operatorv1.ProxyEnvPropagationPolicyPropagate, cfg.Spec.Proxy.EnvPropagationPolicy)
	})
}

func TestLoad_AutomaticRecovery(t *testing.T) {
	t.Run("omitted automaticRecovery block defaults to Enabled/1200", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.AutomaticRecovery)
		assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.AutomaticRecovery.Mode)
		assert.Equal(t, 1200, cfg.Spec.AutomaticRecovery.Delay)
	})

	t.Run("explicit Disabled mode is preserved", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				AutomaticRecovery: &operatorv1.AutomaticRecoveryConfig{
					Mode:  operatorv1.FeatureModeDisabled,
					Delay: 600,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.AutomaticRecovery)
		assert.Equal(t, operatorv1.FeatureModeDisabled, cfg.Spec.AutomaticRecovery.Mode)
		assert.Equal(t, 600, cfg.Spec.AutomaticRecovery.Delay)
	})

	t.Run("present block with omitted delay defaults to 1200", func(t *testing.T) {
		// delay's minimum is 1, so a zero value only occurs when the field is omitted and is
		// sentinel-defaulted to 1200.
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				AutomaticRecovery: &operatorv1.AutomaticRecoveryConfig{
					Mode: operatorv1.FeatureModeDisabled,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.AutomaticRecovery)
		assert.Equal(t, operatorv1.FeatureModeDisabled, cfg.Spec.AutomaticRecovery.Mode)
		assert.Equal(t, 1200, cfg.Spec.AutomaticRecovery.Delay)
	})
}

func TestLoad_MemberClusterClientTimeout(t *testing.T) {
	t.Run("omitted multiCluster block defaults to 10", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.MultiCluster)
		assert.Equal(t, 10, cfg.Spec.MultiCluster.MemberClusterClientTimeout)
	})

	t.Run("explicit value is preserved", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				MultiCluster: &operatorv1.MultiClusterConfig{
					MemberClusterClientTimeout: 42,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.MultiCluster)
		assert.Equal(t, 42, cfg.Spec.MultiCluster.MemberClusterClientTimeout)
	})
}

func TestLoad_MemberClusterRequiredHealthyStreak(t *testing.T) {
	t.Run("omitted multiCluster block defaults to 5", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.MultiCluster)
		assert.Equal(t, 5, cfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak)
	})

	t.Run("explicit value is preserved", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				MultiCluster: &operatorv1.MultiClusterConfig{
					MemberClusterRequiredHealthyStreak: 7,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.MultiCluster)
		assert.Equal(t, 7, cfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak)
	})
}

func TestLoad_Telemetry(t *testing.T) {
	t.Run("explicit Disabled master mode is preserved", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Mode: operatorv1.FeatureModeDisabled,
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.Telemetry)
		assert.Equal(t, operatorv1.FeatureModeDisabled, cfg.Spec.Telemetry.Mode)
		// Nested blocks are still materialised and defaulted even when the master switch is off.
		require.NotNil(t, cfg.Spec.Telemetry.Collection)
		require.NotNil(t, cfg.Spec.Telemetry.Collection.Frequency)
		assert.Equal(t, time.Hour, cfg.Spec.Telemetry.Collection.Frequency.Duration)
		require.NotNil(t, cfg.Spec.Telemetry.Send)
		assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Send.Mode)
	})

	t.Run("explicit nested values are preserved and omitted siblings defaulted", func(t *testing.T) {
		cr := &operatorv1.OperatorConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DefaultOperatorConfigName,
				Namespace: testNamespace,
			},
			Spec: operatorv1.OperatorConfigSpec{
				Telemetry: &operatorv1.TelemetryConfig{
					Collection: &operatorv1.TelemetryCollectionConfig{
						Frequency: &metav1.Duration{Duration: 30 * time.Minute},
						Clusters: &operatorv1.TelemetryCollectionClustersConfig{
							Mode: operatorv1.FeatureModeDisabled,
						},
					},
					Send: &operatorv1.TelemetrySendConfig{
						Mode: operatorv1.FeatureModeDisabled,
					},
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cr).Build()

		cfg, err := Load(context.Background(), c, testNamespace, util.DefaultOperatorConfigName)

		require.NoError(t, err)
		require.NotNil(t, cfg.Spec.Telemetry)
		// Master switch defaults to Enabled when omitted.
		assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Mode)
		// Explicit collection frequency and clusters mode are preserved.
		assert.Equal(t, 30*time.Minute, cfg.Spec.Telemetry.Collection.Frequency.Duration)
		assert.Equal(t, operatorv1.FeatureModeDisabled, cfg.Spec.Telemetry.Collection.Clusters.Mode)
		// Omitted siblings are materialised and defaulted.
		require.NotNil(t, cfg.Spec.Telemetry.Collection.KubeTimeout)
		assert.Equal(t, 5*time.Minute, cfg.Spec.Telemetry.Collection.KubeTimeout.Duration)
		require.NotNil(t, cfg.Spec.Telemetry.Collection.Deployments)
		assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Collection.Deployments.Mode)
		require.NotNil(t, cfg.Spec.Telemetry.Collection.Operators)
		assert.Equal(t, operatorv1.FeatureModeEnabled, cfg.Spec.Telemetry.Collection.Operators.Mode)
		// Explicit send mode preserved; send frequency defaulted.
		assert.Equal(t, operatorv1.FeatureModeDisabled, cfg.Spec.Telemetry.Send.Mode)
		require.NotNil(t, cfg.Spec.Telemetry.Send.Frequency)
		assert.Equal(t, 168*time.Hour, cfg.Spec.Telemetry.Send.Frequency.Duration)
	})
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
