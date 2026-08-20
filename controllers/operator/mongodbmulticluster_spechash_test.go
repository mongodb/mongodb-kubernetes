package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

func mustSpecHash(t *testing.T, spec mdbmulti.MongoDBMultiSpec) string {
	hash, err := multiClusterSpecHash(spec)
	require.NoError(t, err)
	return hash
}

func TestMultiClusterSpecHashCanonicalization(t *testing.T) {
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	t.Run("api server round-trip does not change the hash", func(t *testing.T) {
		// the round-trip normalizes e.g. a nil additionalMongodConfig into {}; canonicalization
		// puts null, {} and absent in one equivalence class, so both forms hash identically
		direct := mustSpecHash(t, m.Spec)
		c := mock.NewEmptyFakeClientBuilder().WithObjects(m.DeepCopy()).Build()
		read := mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, c.Get(context.Background(), kube.ObjectKey(m.Namespace, m.Name), &read))
		assert.Equal(t, direct, mustSpecHash(t, read.Spec))
	})

	t.Run("content changes change the hash", func(t *testing.T) {
		changed := m.DeepCopy()
		changed.Spec.ClusterSpecList[0].Members++
		assert.NotEqual(t, mustSpecHash(t, m.Spec), mustSpecHash(t, changed.Spec))
	})

	t.Run("scalar zero values are content, not noise", func(t *testing.T) {
		changed := m.DeepCopy()
		changed.Spec.Version = ""
		assert.NotEqual(t, mustSpecHash(t, m.Spec), mustSpecHash(t, changed.Spec))
	})
}
