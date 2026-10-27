package migration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	testPodName       = "mongodb-kubernetes-operator-abc123"
	testContainerName = "mongodb-kubernetes-operator"
	envImage          = "quay.io/mongodb/mongodb-kubernetes:1.2.3"
	podImage          = "quay.io/mongodb/mongodb-kubernetes:from-pod"
)

func operatorPod(containerName, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: containerName, Image: image}},
		},
	}
}

func newPodClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestResolveOperatorImage(t *testing.T) {
	t.Run("returns the image when MDB_OPERATOR_IMAGE is set", func(t *testing.T) {
		img, st := ResolveOperatorImage(images.ImageUrls{util.OperatorImageEnv: envImage})
		assert.True(t, st.IsOK())
		assert.Equal(t, envImage, img)
	})

	t.Run("fails with OperatorImageUnknown when unset", func(t *testing.T) {
		img, st := ResolveOperatorImage(images.ImageUrls{})
		assert.Empty(t, img)
		require.False(t, st.IsOK())

		opts := st.StatusOptions()
		require.NotEmpty(t, opts)
		found := false
		for _, o := range opts {
			if cond, ok := o.Value().(metav1.Condition); ok {
				if cond.Reason == "OperatorImageUnknown" {
					found = true
					assert.Equal(t, mdbstatus.ConditionNetworkConnectivityVerified, cond.Type)
					assert.Equal(t, metav1.ConditionFalse, cond.Status)
				}
			}
		}
		assert.True(t, found, "expected an OperatorImageUnknown migration condition")
	})
}

func TestImageFromOperatorPod(t *testing.T) {
	ctx := context.Background()

	t.Run("reads the named container image from the pod", func(t *testing.T) {
		c := newPodClient(t, operatorPod(testContainerName, podImage))
		assert.Equal(t, podImage, ImageFromOperatorPod(ctx, c, namespace, testPodName, testContainerName))
	})

	t.Run("falls back to the sole container when the name is empty", func(t *testing.T) {
		c := newPodClient(t, operatorPod("only-container", podImage))
		assert.Equal(t, podImage, ImageFromOperatorPod(ctx, c, namespace, testPodName, ""))
	})

	t.Run("returns empty when the container is not found", func(t *testing.T) {
		c := newPodClient(t, operatorPod("some-other-container", podImage))
		assert.Empty(t, ImageFromOperatorPod(ctx, c, namespace, testPodName, testContainerName))
	})

	t.Run("returns empty when the pod is not found", func(t *testing.T) {
		c := newPodClient(t)
		assert.Empty(t, ImageFromOperatorPod(ctx, c, namespace, testPodName, testContainerName))
	})

	t.Run("returns empty without a Get when POD_NAME is empty", func(t *testing.T) {
		c := newPodClient(t, operatorPod(testContainerName, podImage))
		assert.Empty(t, ImageFromOperatorPod(ctx, c, namespace, "", testContainerName))
	})

	t.Run("returns empty when the reader is nil", func(t *testing.T) {
		assert.Empty(t, ImageFromOperatorPod(ctx, nil, namespace, testPodName, testContainerName))
	})
}
