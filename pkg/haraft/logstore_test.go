package haraft

import (
	"testing"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeLogStore(t *testing.T) *ConfigMapLogStore {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: StateConfigMapName, Namespace: "ns"}}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(cm).Build()
	return NewConfigMapLogStore(c, "ns")
}

func TestLogStore_EmptyFirstLast(t *testing.T) {
	s := newFakeLogStore(t)
	first, err := s.FirstIndex()
	require.NoError(t, err)
	assert.Equal(t, uint64(0), first)
	last, err := s.LastIndex()
	require.NoError(t, err)
	assert.Equal(t, uint64(0), last)
}

func TestLogStore_StoreAndGet(t *testing.T) {
	s := newFakeLogStore(t)
	entry := &raft.Log{Index: 1, Term: 1, Type: raft.LogNoop}
	require.NoError(t, s.StoreLog(entry))

	got := &raft.Log{}
	require.NoError(t, s.GetLog(1, got))
	assert.Equal(t, uint64(1), got.Index)
	assert.Equal(t, uint64(1), got.Term)
	assert.Equal(t, raft.LogNoop, got.Type)

	first, _ := s.FirstIndex()
	last, _ := s.LastIndex()
	assert.Equal(t, uint64(1), first)
	assert.Equal(t, uint64(1), last)
}

func TestLogStore_DeleteRange(t *testing.T) {
	s := newFakeLogStore(t)
	for i := uint64(1); i <= 5; i++ {
		require.NoError(t, s.StoreLog(&raft.Log{Index: i, Term: 1}))
	}
	require.NoError(t, s.DeleteRange(2, 4))
	got := &raft.Log{}
	assert.ErrorIs(t, s.GetLog(3, got), raft.ErrLogNotFound)
	first, _ := s.FirstIndex()
	last, _ := s.LastIndex()
	assert.Equal(t, uint64(1), first)
	assert.Equal(t, uint64(5), last)
}
