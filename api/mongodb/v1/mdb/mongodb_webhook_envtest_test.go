package mdb_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ctrl "sigs.k8s.io/controller-runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const (
	mongodbWebhookName   = "validate-mongodb.mongodb.com"
	webhookTestNamespace = "default"
)

var replicaSetHorizons = []mdbv1.MongoDBHorizonConfig{
	{"internal": "my-rs-0.example.com:27017"},
	{"internal": "my-rs-1.example.com:27017"},
	{"internal": "my-rs-2.example.com:27017"},
}

func startMongoDBWebhookServer(t *testing.T) *env.TestEnv {
	t.Helper()
	shared := env.Shared(t)
	shared.StartWebhookServer(t, func(mgr ctrl.Manager) error {
		return ctrl.NewWebhookManagedBy(mgr).For(&mdbv1.MongoDB{}).WithValidator(&mdbv1.MongoDBValidator{}).Complete()
	})
	return shared
}

func newReplicaSet(name string) *mdbv1.MongoDB {
	return mdbv1.NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetName(name).SetNamespace(webhookTestNamespace).Build()
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

func TestMongoDBWebhook_StandaloneDeprecationWarning(t *testing.T) {
	ctx := context.Background()
	shared := startMongoDBWebhookServer(t)

	warnings := &warningCollector{}
	warnCfg := rest.CopyConfig(shared.Config)
	warnCfg.WarningHandler = warnings
	warnClient, err := client.New(warnCfg, client.Options{Scheme: shared.Client.Scheme()})
	require.NoError(t, err)

	standalone := mdbv1.NewStandaloneBuilder().AddDummyOpsManagerConfig().Build()
	standalone.Name = "test-standalone"
	standalone.Namespace = webhookTestNamespace
	t.Cleanup(func() { _ = warnClient.Delete(ctx, standalone) })
	require.NoError(t, warnClient.Create(ctx, standalone))
	assert.Contains(t, warnings.collected(), mdbv1.StandaloneDeprecationMessage)

	beforeUpdate := len(warnings.collected())
	standalone.Labels = map[string]string{"test": "update"}
	require.NoError(t, warnClient.Update(ctx, standalone))
	assert.Greater(t, len(warnings.collected()), beforeUpdate)

	rs := newReplicaSet("test-replica-set")
	t.Cleanup(func() { _ = warnClient.Delete(ctx, rs) })
	beforeReplicaSet := len(warnings.collected())
	require.NoError(t, warnClient.Create(ctx, rs))
	assert.Len(t, warnings.collected(), beforeReplicaSet, "a ReplicaSet must not produce the Standalone warning")
}

func TestMongoDBWebhook_RejectsInvalidResources(t *testing.T) {
	ctx := context.Background()
	shared := startMongoDBWebhookServer(t)

	horizonsWithoutTLS := newReplicaSet("invalid-horizons")
	horizonsWithoutTLS.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons

	tests := []struct {
		name          string
		object        *mdbv1.MongoDB
		update        func(*mdbv1.MongoDB)
		expectedError string
	}{
		{
			name:   "valid replica set is accepted",
			object: newReplicaSet("valid-replica-set"),
		},
		{
			name:          "replica set horizons without TLS are rejected",
			object:        horizonsWithoutTLS,
			expectedError: "TLS must be enabled in order to use replica set horizons",
		},
		{
			name:          "changing downloadBase is rejected",
			object:        newReplicaSet("immutable-download-base"),
			update:        func(rs *mdbv1.MongoDB) { rs.Spec.DownloadBase = "/custom/download/base" },
			expectedError: "'spec.downloadBase' cannot be changed once created",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { _ = shared.Client.Delete(ctx, tc.object) })

			err := shared.Client.Create(ctx, tc.object)
			if tc.update != nil {
				require.NoError(t, err, "the baseline object must be accepted")
				tc.update(tc.object)
				err = shared.Client.Update(ctx, tc.object)
			}

			if tc.expectedError == "" {
				require.NoError(t, err)
				return
			}
			env.RequireDeniedByWebhook(t, err, mongodbWebhookName, tc.expectedError)
		})
	}
}
