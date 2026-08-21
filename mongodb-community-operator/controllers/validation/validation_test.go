package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

// TestValidateMemberCount covers KUBE-308: spec.members is used as an allocation and loop bound
// (make([]string, members) when building the mongot host seeds) before any effective validation.
// A large-positive count allocates multiple GB with no panic, so the operator is OOMKilled by its
// cgroup, which controller-runtime's RecoverPanic cannot intercept. The poisoned object persists in
// etcd, so the operator is re-killed on every restart.
func TestValidateMemberCount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		members   int
		expectErr string
	}{
		{"large positive", 1_000_000_000, "spec.members must be between 0 and 50, got 1000000000"},
		{"negative", -1, "spec.members must be between 0 and 50, got -1"},
		{"just above max", 51, "spec.members must be between 0 and 50, got 51"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mdb := mdbv1.MongoDBCommunity{Spec: mdbv1.MongoDBCommunitySpec{Members: tc.members}}
			err := validateMemberCount(mdb)
			require.Error(t, err)
			assert.Equal(t, tc.expectErr, err.Error())
		})
	}

	for _, members := range []int{0, 1, 3, 50} {
		mdb := mdbv1.MongoDBCommunity{Spec: mdbv1.MongoDBCommunitySpec{Members: members}}
		assert.NoError(t, validateMemberCount(mdb))
	}
}
