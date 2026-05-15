package haraft

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeStore(t *testing.T) *ConfigMapStableStore {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: StateConfigMapName, Namespace: "ns"},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cm).Build()
	return NewConfigMapStableStore(c, "ns")
}

func TestStableStore_SetGet(t *testing.T) {
	s := newFakeStore(t)
	require.NoError(t, s.Set([]byte("CurrentTerm"), []byte("3")))
	got, err := s.Get([]byte("CurrentTerm"))
	require.NoError(t, err)
	assert.Equal(t, []byte("3"), got)
}

func TestStableStore_GetMissing(t *testing.T) {
	s := newFakeStore(t)
	got, err := s.Get([]byte("absent"))
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStableStore_Uint64(t *testing.T) {
	s := newFakeStore(t)
	require.NoError(t, s.SetUint64([]byte("k"), 42))
	v, err := s.GetUint64([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, uint64(42), v)
}
