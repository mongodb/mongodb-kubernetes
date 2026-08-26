package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	coordinationv1 "k8s.io/api/coordination/v1"
)

func TestLeaseTermRoundTrip(t *testing.T) {
	lease := &coordinationv1.Lease{}

	_, ok := leaseTerm(lease)
	assert.False(t, ok, "no annotations at all reads as no term")

	setLeaseTerm(lease, 7)
	term, ok := leaseTerm(lease)
	assert.True(t, ok)
	assert.Equal(t, int64(7), term)

	setLeaseTerm(lease, 8)
	term, _ = leaseTerm(lease)
	assert.Equal(t, int64(8), term, "restamping overwrites")
}

func TestLeaseTermPreservesOtherAnnotations(t *testing.T) {
	lease := &coordinationv1.Lease{}
	lease.Annotations = map[string]string{"unrelated": "kept"}

	setLeaseTerm(lease, 3)

	assert.Equal(t, "kept", lease.Annotations["unrelated"])
	assert.Equal(t, "3", lease.Annotations[leadershipTermAnnotation])
}

func TestLeaseTermMalformed(t *testing.T) {
	lease := &coordinationv1.Lease{}
	lease.Annotations = map[string]string{leadershipTermAnnotation: "not-a-number"}

	term, ok := leaseTerm(lease)
	assert.False(t, ok)
	assert.Equal(t, int64(0), term)
}
