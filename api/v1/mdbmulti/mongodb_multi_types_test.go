package mdbmulti

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func TestMongoDBMultiSpecMinimumMajorVersion(t *testing.T) {
	tests := []struct {
		name         string
		DbCommonSpec mdb.DbCommonSpec
		want         uint64
	}{
		{
			name: "non ent",
			DbCommonSpec: mdb.DbCommonSpec{
				Version: "7.1.0",
			},
			want: 7,
		},
		{
			name: "ent",
			DbCommonSpec: mdb.DbCommonSpec{
				Version: "7.0.2-ent",
			},
			want: 7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MongoDBMultiSpec{
				DbCommonSpec: tt.DbCommonSpec,
			}
			assert.Equalf(t, tt.want, m.MinimumMajorVersion(), "MinimumMajorVersion()")
		})
	}
}
