package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

func TestStaticElector(t *testing.T) {
	deployment := types.NamespacedName{Namespace: "mongodb-test", Name: "multi-replica-set"}

	term, isLeader := NewStaticElector("kind-e2e-cluster-1", "kind-e2e-cluster-1").Current(deployment)
	assert.True(t, isLeader)
	assert.Equal(t, staticElectorTerm, term)

	term, isLeader = NewStaticElector("kind-e2e-cluster-2", "kind-e2e-cluster-1").Current(deployment)
	assert.False(t, isLeader)
	assert.Equal(t, staticElectorTerm, term)

	// an operator without identity (hub-and-spoke mode) is never leader
	_, isLeader = NewStaticElector("", "").Current(deployment)
	assert.False(t, isLeader)
}
