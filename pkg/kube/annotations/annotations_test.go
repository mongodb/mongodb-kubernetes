package annotations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func newCM(annos map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cm",
			Namespace:   "ns",
			Annotations: annos,
		},
	}
}

func getCM(t *testing.T, c client.Client) *corev1.ConfigMap {
	t.Helper()
	out := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cm", Namespace: "ns"}, out))
	return out
}

func TestRemoveAnnotation_RemovesExistingKey(t *testing.T) {
	ctx := context.Background()
	cm := newCM(map[string]string{
		"target":  "value",
		"sibling": "keep-me",
	})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()

	require.NoError(t, RemoveAnnotation(ctx, cm, "target", c))

	got := getCM(t, c)
	_, present := got.Annotations["target"]
	assert.False(t, present, "target annotation should be removed")
	assert.Equal(t, "keep-me", got.Annotations["sibling"], "sibling annotation must be preserved")
	_, presentLocal := cm.Annotations["target"]
	assert.False(t, presentLocal, "in-memory object should reflect removal")
}

func TestRemoveAnnotation_MissingKeyIsNoOp(t *testing.T) {
	ctx := context.Background()
	cm := newCM(map[string]string{"sibling": "keep-me"})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()

	require.NoError(t, RemoveAnnotation(ctx, cm, "absent", c))

	got := getCM(t, c)
	assert.Equal(t, map[string]string{"sibling": "keep-me"}, got.Annotations)
}

func TestRemoveAnnotation_NoAnnotationsIsNoOp(t *testing.T) {
	ctx := context.Background()
	cm := newCM(nil)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()

	require.NoError(t, RemoveAnnotation(ctx, cm, "anything", c))

	got := getCM(t, c)
	assert.Empty(t, got.Annotations)
}

func TestRemoveAnnotation_LastKeyLeavesEmptyMap(t *testing.T) {
	ctx := context.Background()
	cm := newCM(map[string]string{"only": "value"})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()

	require.NoError(t, RemoveAnnotation(ctx, cm, "only", c))

	got := getCM(t, c)
	_, present := got.Annotations["only"]
	assert.False(t, present, "removed key should not be present")
}

func TestRemoveAnnotation_KeyWithSlashIsEscaped(t *testing.T) {
	ctx := context.Background()
	cm := newCM(map[string]string{"mongodb.com/v1.foo": "value", "sibling": "keep-me"})
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()

	require.NoError(t, RemoveAnnotation(ctx, cm, "mongodb.com/v1.foo", c))

	got := getCM(t, c)
	_, present := got.Annotations["mongodb.com/v1.foo"]
	assert.False(t, present, "slashed key should be removed (path-escaped)")
	assert.Equal(t, "keep-me", got.Annotations["sibling"])
}
