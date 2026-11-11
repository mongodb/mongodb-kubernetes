package mdbmulti_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	mcdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const mongodbMultiClusterWebhookName = "validate-mongodbmulticluster.mongodb.com"

func newWebhookMultiCluster(name string, clusterNames []string) *mcdbmulti.MongoDBMultiCluster {
	mrs := mcdbmulti.DefaultMultiReplicaSetBuilder().SetName(name).SetClusterSpecList(clusterNames).Build()
	mrs.Namespace = "default"
	return mrs
}

// The webhook configuration ignores failures, so an unregistered path would accept the
// invalid objects: every rejected row only passes if the webhook is actually reached.
func TestMongoDBMultiClusterWebhook_RejectsInvalidResources(t *testing.T) {
	ctx := context.Background()
	shared := env.Shared(t)
	shared.StartWebhookServer(t, func(mgr ctrl.Manager) error {
		return ctrl.NewWebhookManagedBy(mgr).For(&mcdbmulti.MongoDBMultiCluster{}).WithValidator(&mcdbmulti.MongoDBMultiClusterValidator{}).Complete()
	})

	tests := []struct {
		name          string
		object        *mcdbmulti.MongoDBMultiCluster
		expectedError string
	}{
		{
			name:   "valid multi-cluster resource is accepted",
			object: newWebhookMultiCluster("valid-multi-cluster", []string{"cluster-1", "cluster-2"}),
		},
		{
			name:          "empty clusterSpecList is rejected",
			object:        newWebhookMultiCluster("invalid-multi-cluster", nil),
			expectedError: env.DeniedByWebhook(mongodbMultiClusterWebhookName, "ClusterSpecList empty is not allowed, please define at least one cluster"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { _ = shared.Client.Delete(ctx, tc.object) })

			err := shared.Client.Create(ctx, tc.object)

			if tc.expectedError == "" {
				require.NoError(t, err)
				return
			}
			assert.True(t, apierrors.IsForbidden(err), "expected the admission webhook to deny the request, got: %v", err)
			require.EqualError(t, err, tc.expectedError)
		})
	}
}
