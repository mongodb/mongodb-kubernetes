package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

func TestValidateArbiterSpec(t *testing.T) {
	tests := map[string]struct {
		members   int
		arbiters  int
		expectErr bool
	}{
		"Members without arbiters":            {members: 3, arbiters: 0},
		"Fewer arbiters than members":         {members: 3, arbiters: 1},
		"Zero members and zero arbiters":      {members: 0, arbiters: 0},
		"As many arbiters as members":         {members: 3, arbiters: 3, expectErr: true},
		"Arbiters without members":            {members: 0, arbiters: 1, expectErr: true},
		"Negative number of arbiters":         {members: 3, arbiters: -1, expectErr: true},
		"Negative arbiters with zero members": {members: 0, arbiters: -1, expectErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			mdb := mdbv1.MongoDBCommunity{Spec: mdbv1.MongoDBCommunitySpec{Members: tc.members, Arbiters: tc.arbiters}}
			err := validateArbiterSpec(mdb)
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
