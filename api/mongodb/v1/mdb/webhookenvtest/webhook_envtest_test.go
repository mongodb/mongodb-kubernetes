package webhookenvtest

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	api "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const testNamespace = "default"

// The webhook tests need their own package because the control plane must install
// the webhook configuration, which the shared environments of the other packages
// do not, their tests create resources the webhook rejects.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m,
		env.WithCRDs(
			"mongodb.com_mongodb.yaml",
			"mongodb.com_mongodbmulticluster.yaml",
			"mongodb.com_opsmanagers.yaml",
		),
		env.WithWebhooks("webhooks.yaml"),
	))
}

// warningCollector implements rest.WarningHandler. Admission webhook warnings arrive
// in the Warning response header with code 299, the API server relays them to clients.
type warningCollector struct {
	mu       sync.Mutex
	warnings []string
}

func (w *warningCollector) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warnings = append(w.warnings, text)
}

func (w *warningCollector) collected() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.warnings...)
}

// webhookServerOnce keeps the manager alive for the whole test binary: envtest reserves
// the webhook port once per package, so every test shares the one server.
var webhookServerOnce sync.Once

// startWebhookServer runs a manager serving all validating webhooks on the port
// envtest reserved when it installed the webhook configuration.
func startWebhookServer(t *testing.T, shared *env.TestEnv) {
	t.Helper()

	webhookServerOnce.Do(func() {
		scheme := runtime.NewScheme()
		require.NoError(t, api.AddToScheme(scheme))

		webhookOptions := shared.Environment.WebhookInstallOptions
		mgr, err := ctrl.NewManager(shared.Config, ctrl.Options{
			Scheme: scheme,
			WebhookServer: webhook.NewServer(webhook.Options{
				Host:    webhookOptions.LocalServingHost,
				Port:    webhookOptions.LocalServingPort,
				CertDir: webhookOptions.LocalServingCertDir,
			}),
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
		})
		require.NoError(t, err)
		require.NoError(t, ctrl.NewWebhookManagedBy(mgr).For(&mdbv1.MongoDB{}).WithValidator(&mdbv1.MongoDBValidator{}).Complete())
		require.NoError(t, ctrl.NewWebhookManagedBy(mgr).For(&mdbmultiv1.MongoDBMultiCluster{}).WithValidator(&mdbmultiv1.MongoDBMultiClusterValidator{}).Complete())
		require.NoError(t, ctrl.NewWebhookManagedBy(mgr).For(&omv1.MongoDBOpsManager{}).WithValidator(&omv1.MongoDBOpsManagerValidator{}).Complete())

		// The manager is never stopped: it serves the webhook until the test binary exits.
		go func() {
			if err := mgr.Start(context.Background()); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "webhook manager failed: %v\n", err)
			}
		}()

		require.Eventually(t, func() bool {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", webhookOptions.LocalServingHost, webhookOptions.LocalServingPort), time.Second)
			if err != nil {
				return false
			}
			_ = conn.Close()
			return true
		}, 30*time.Second, 100*time.Millisecond, "webhook server did not become reachable")
	})
}

// newWarningClient returns a client that records the warnings the API server returns.
func newWarningClient(t *testing.T, shared *env.TestEnv) (client.Client, *warningCollector) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))

	collector := &warningCollector{}
	warnCfg := rest.CopyConfig(shared.Config)
	warnCfg.WarningHandler = collector
	warnClient, err := client.New(warnCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	return warnClient, collector
}

func TestMongoDBWebhook_StandaloneDeprecationWarning(t *testing.T) {
	ctx := context.Background()
	shared := env.Shared(t)
	startWebhookServer(t, shared)
	warnClient, warnings := newWarningClient(t, shared)

	standalone := mdbv1.NewStandaloneBuilder().AddDummyOpsManagerConfig().Build()
	standalone.Name = "test-standalone"
	standalone.Namespace = testNamespace
	t.Cleanup(func() { _ = warnClient.Delete(ctx, standalone) })
	require.NoError(t, warnClient.Create(ctx, standalone))
	assert.Contains(t, warnings.collected(), mdbv1.StandaloneDeprecationMessage)

	beforeUpdate := len(warnings.collected())
	standalone.Labels = map[string]string{"test": "update"}
	require.NoError(t, warnClient.Update(ctx, standalone))
	assert.Greater(t, len(warnings.collected()), beforeUpdate)

	rs := mdbv1.NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
	rs.Name = "test-replica-set"
	rs.Namespace = testNamespace
	t.Cleanup(func() { _ = warnClient.Delete(ctx, rs) })
	beforeReplicaSet := len(warnings.collected())
	require.NoError(t, warnClient.Create(ctx, rs))
	assert.Len(t, warnings.collected(), beforeReplicaSet, "a ReplicaSet must not produce the Standalone warning")
}

const (
	mongodbWebhookName             = "validate-mongodb.mongodb.com"
	mongodbMultiClusterWebhookName = "validate-mongodbmulticluster.mongodb.com"
	opsManagerWebhookName          = "validate-opsmanagers.mongodb.com"
)

func deniedByWebhook(webhookName, message string) string {
	return fmt.Sprintf(`admission webhook %q denied the request: %s`, webhookName, message)
}

var replicaSetHorizons = []mdbv1.MongoDBHorizonConfig{
	{"internal": "my-rs-0.example.com:27017"},
	{"internal": "my-rs-1.example.com:27017"},
	{"internal": "my-rs-2.example.com:27017"},
}

func newReplicaSet(name string) *mdbv1.MongoDB {
	return mdbv1.NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetName(name).SetNamespace(testNamespace).Build()
}

func newMultiCluster(name string, clusterNames []string) *mdbmultiv1.MongoDBMultiCluster {
	ms := mdbmultiv1.DefaultMultiReplicaSetBuilder().SetName(name).SetClusterSpecList(clusterNames).Build()
	ms.Namespace = testNamespace
	return ms
}

// newOpsManager returns an Ops Manager with an external AppDB, which the webhook
// requires to be named after the resource.
func newOpsManager(name, version string) *omv1.MongoDBOpsManager {
	return omv1.NewOpsManagerBuilder().
		SetName(name).
		SetNamespace(testNamespace).
		SetVersion(version).
		SetAppDBToNil().
		SetExternalAppDBRef(omv1.ExternalAppDBRef{Name: name + "-db", Kind: "MongoDB"}).
		Build()
}

// TestWebhook_RejectsInvalidResources proves each webhook is actually reached: the
// configuration ignores webhook failures, so an unregistered path would accept the
// invalid object. Every rejected row comes with an accepted baseline of the same kind.
func TestWebhook_RejectsInvalidResources(t *testing.T) {
	shared := env.Shared(t)
	k8sClient := shared.Client
	ctx := context.Background()
	startWebhookServer(t, shared)

	horizonsWithoutTLS := newReplicaSet("invalid-horizons")
	horizonsWithoutTLS.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons

	tests := []struct {
		name          string
		object        client.Object
		update        func(client.Object)
		expectedError string
	}{
		{
			name:   "MongoDB is accepted",
			object: newReplicaSet("valid-replica-set"),
		},
		{
			name:          "MongoDB with replica set horizons but without TLS is rejected",
			object:        horizonsWithoutTLS,
			expectedError: deniedByWebhook(mongodbWebhookName, "TLS must be enabled in order to use replica set horizons"),
		},
		{
			name:          "MongoDB update changing downloadBase is rejected",
			object:        newReplicaSet("immutable-download-base"),
			update:        func(obj client.Object) { obj.(*mdbv1.MongoDB).Spec.DownloadBase = "/custom/download/base" },
			expectedError: deniedByWebhook(mongodbWebhookName, "'spec.downloadBase' cannot be changed once created"),
		},
		{
			name:   "MongoDBMultiCluster is accepted",
			object: newMultiCluster("valid-multi-cluster", []string{"cluster-1", "cluster-2"}),
		},
		{
			name:          "MongoDBMultiCluster without clusterSpecList is rejected",
			object:        newMultiCluster("invalid-multi-cluster", nil),
			expectedError: deniedByWebhook(mongodbMultiClusterWebhookName, "ClusterSpecList empty is not allowed, please define at least one cluster"),
		},
		{
			name:   "MongoDBOpsManager is accepted",
			object: newOpsManager("valid-ops-manager", "8.0.19"),
		},
		{
			name:          "MongoDBOpsManager with an invalid version is rejected",
			object:        newOpsManager("invalid-ops-manager", "4.4.4.4"),
			expectedError: deniedByWebhook(opsManagerWebhookName, "'4.4.4.4' is an invalid value for spec.version: Ops Manager Status spec.version 4.4.4.4 is invalid"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, tc.object) })

			var err error
			if tc.update == nil {
				err = k8sClient.Create(ctx, tc.object)
			} else {
				require.NoError(t, k8sClient.Create(ctx, tc.object), "the baseline object must be accepted")
				tc.update(tc.object)
				err = k8sClient.Update(ctx, tc.object)
			}

			if tc.expectedError == "" {
				require.NoError(t, err)
				return
			}
			assert.True(t, apierrors.IsForbidden(err), "expected the admission webhook to deny the request, got: %v", err)
			require.EqualError(t, err, tc.expectedError)
		})
	}
}
