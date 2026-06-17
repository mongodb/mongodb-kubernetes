package commoncontroller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

func TestStatusSubresourcePatchPaths(t *testing.T) {
	tests := []struct {
		name     string
		fullPath string
		want     []string
	}{
		{name: "status root", fullPath: "/status", want: []string{"/status"}},
		{name: "nested substatus", fullPath: "/status/loadBalancer", want: []string{"/status", "/status/loadBalancer"}},
		{name: "deeply nested substatus", fullPath: "/status/opsManager/backup", want: []string{"/status", "/status/opsManager", "/status/opsManager/backup"}},
		{name: "empty path", fullPath: "", want: nil},
		{name: "bare slash never patches the document root", fullPath: "/", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusSubresourcePatchPaths(tc.fullPath)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "/", "a JSON-patch add at the root replaces the entire status")
		})
	}
}

// Bootstrapping /status/loadBalancer must create only the missing members:
// JSON-patch add REPLACES an existing member, so adding an existing /status
// would wipe sibling fields written by the main controller.
func TestEnsureStatusSubresourceExists_DoesNotClobberExistingStatus(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus map[string]interface{}
	}{
		{name: "status missing entirely", initialStatus: nil},
		{
			name:          "status exists without loadBalancer",
			initialStatus: map[string]interface{}{"phase": "Running", "version": "1.2.3"},
		},
		{
			name: "status and loadBalancer both exist",
			initialStatus: map[string]interface{}{
				"phase":        "Running",
				"version":      "1.2.3",
				"loadBalancer": map[string]interface{}{"phase": "Pending", "message": "deploying"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Seeded as unstructured: a typed object always marshals "status",
			// so only unstructured can represent a CR without it.
			obj := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "mongodb.com/v1",
				"kind":       "MongoDBSearch",
				"metadata":   map[string]interface{}{"name": "mdbs", "namespace": "ns"},
				"spec":       map[string]interface{}{},
			}}
			if tc.initialStatus != nil {
				obj.Object["status"] = tc.initialStatus
			}
			scheme := runtime.NewScheme()
			require.NoError(t, v1.AddToScheme(scheme))
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&searchv1.MongoDBSearch{}).
				WithObjects(obj).
				Build()

			search := &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "mdbs", Namespace: "ns"}}
			require.NoError(t, ensureStatusSubresourceExists(ctx, kubernetesClient.NewClient(c), search,
				searchv1.NewSearchPartOption(searchv1.SearchPartLoadBalancer)))

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(obj.GroupVersionKind())
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "mdbs", Namespace: "ns"}, got))
			gotStatus, ok := got.Object["status"].(map[string]interface{})
			require.True(t, ok, "/status must exist after ensure")
			assert.Contains(t, gotStatus, "loadBalancer", "/status/loadBalancer must exist after ensure")

			if tc.initialStatus == nil {
				return
			}
			assert.Equal(t, "Running", gotStatus["phase"], "sentinel status field must survive the loadBalancer bootstrap")
			assert.Equal(t, "1.2.3", gotStatus["version"], "sentinel status field must survive the loadBalancer bootstrap")
			if lb, exists := tc.initialStatus["loadBalancer"]; exists {
				assert.Equal(t, lb, gotStatus["loadBalancer"], "existing loadBalancer content must be untouched")
			}
		})
	}
}
