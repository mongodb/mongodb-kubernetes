package om_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

const opsManagerWebhookName = "validate-opsmanagers.mongodb.com"

// newWebhookOpsManager returns an Ops Manager with an external AppDB, which the webhook
// requires to be named after the resource.
func newWebhookOpsManager(name, version string) *omv1.MongoDBOpsManager {
	return omv1.NewOpsManagerBuilder().
		SetName(name).
		SetNamespace("default").
		SetVersion(version).
		SetAppDBToNil().
		SetExternalAppDBRef(omv1.ExternalAppDBRef{Name: name + "-db", Kind: "MongoDB"}).
		Build()
}

// The webhook configuration ignores failures, so an unregistered path would accept the
// invalid objects: every rejected row only passes if the webhook is actually reached.
func TestOpsManagerWebhook_RejectsInvalidResources(t *testing.T) {
	ctx := context.Background()
	shared := env.Shared(t)
	shared.StartWebhookServer(t, func(mgr ctrl.Manager) error {
		return ctrl.NewWebhookManagedBy(mgr).For(&omv1.MongoDBOpsManager{}).WithValidator(&omv1.MongoDBOpsManagerValidator{}).Complete()
	})

	tests := []struct {
		name          string
		object        *omv1.MongoDBOpsManager
		expectedError string
	}{
		{
			name:   "valid Ops Manager is accepted",
			object: newWebhookOpsManager("valid-ops-manager", "8.0.19"),
		},
		{
			name:          "invalid version is rejected",
			object:        newWebhookOpsManager("invalid-ops-manager", "4.4.4.4"),
			expectedError: env.DeniedByWebhook(opsManagerWebhookName, "'4.4.4.4' is an invalid value for spec.version: Ops Manager Status spec.version 4.4.4.4 is invalid"),
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
