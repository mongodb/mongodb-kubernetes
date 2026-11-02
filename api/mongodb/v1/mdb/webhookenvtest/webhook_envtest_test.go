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

	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	api "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// The webhook tests need their own package because the control plane must install
// the webhook configuration, which the shared environments of the other packages
// do not, their tests create resources the webhook rejects.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m,
		env.WithCRDs("mongodb.com_mongodb.yaml"),
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

// startWebhookServer runs a manager serving the MongoDB validating webhook on the
// port envtest reserved when it installed the webhook configuration.
func startWebhookServer(t *testing.T, shared *env.TestEnv) {
	t.Helper()

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

	mgrCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil && mgrCtx.Err() == nil {
			t.Errorf("webhook manager failed: %v", err)
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
	standalone.Namespace = "default"
	require.NoError(t, warnClient.Create(ctx, standalone))
	assert.Contains(t, warnings.collected(), mdbv1.StandaloneDeprecationMessage)

	beforeUpdate := len(warnings.collected())
	standalone.Labels = map[string]string{"test": "update"}
	require.NoError(t, warnClient.Update(ctx, standalone))
	assert.Greater(t, len(warnings.collected()), beforeUpdate)

	rs := mdbv1.NewReplicaSetBuilder().AddDummyOpsManagerConfig().Build()
	rs.Name = "test-replica-set"
	rs.Namespace = "default"
	beforeReplicaSet := len(warnings.collected())
	require.NoError(t, warnClient.Create(ctx, rs))
	assert.Len(t, warnings.collected(), beforeReplicaSet, "a ReplicaSet must not produce the Standalone warning")
}
