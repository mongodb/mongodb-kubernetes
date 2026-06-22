package user

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMongoDBUserSpec_ValidateSpec(t *testing.T) {
	t.Run("legacy db field alone is valid", func(t *testing.T) {
		spec := MongoDBUserSpec{Database: "admin"}
		assert.NoError(t, spec.ValidateSpec())
	})

	t.Run("authSource and defaultDatabase together are valid", func(t *testing.T) {
		spec := MongoDBUserSpec{AuthSource: "admin", DefaultDatabase: "myapp"}
		assert.NoError(t, spec.ValidateSpec())
	})

	t.Run("empty spec is valid (controller defaults db to admin)", func(t *testing.T) {
		spec := MongoDBUserSpec{}
		assert.NoError(t, spec.ValidateSpec())
	})

	t.Run("db combined with authSource is invalid", func(t *testing.T) {
		spec := MongoDBUserSpec{Database: "admin", AuthSource: "admin"}
		assert.Error(t, spec.ValidateSpec())
	})

	t.Run("db combined with defaultDatabase is invalid", func(t *testing.T) {
		spec := MongoDBUserSpec{Database: "admin", DefaultDatabase: "myapp"}
		assert.Error(t, spec.ValidateSpec())
	})

	t.Run("authSource without defaultDatabase is invalid", func(t *testing.T) {
		spec := MongoDBUserSpec{AuthSource: "admin"}
		assert.Error(t, spec.ValidateSpec())
	})

	t.Run("defaultDatabase without authSource is invalid", func(t *testing.T) {
		spec := MongoDBUserSpec{DefaultDatabase: "myapp"}
		assert.Error(t, spec.ValidateSpec())
	})
}

func TestMongoDBUserSpec_EffectiveHelpers(t *testing.T) {
	t.Run("legacy db used for both auth and path", func(t *testing.T) {
		spec := MongoDBUserSpec{Database: "admin"}
		assert.Equal(t, "admin", spec.EffectiveAuthDatabase())
		assert.Equal(t, "admin", spec.EffectivePathDatabase())
	})

	t.Run("new fields use authSource for auth and defaultDatabase for path", func(t *testing.T) {
		spec := MongoDBUserSpec{AuthSource: "admin", DefaultDatabase: "myapp"}
		assert.Equal(t, "admin", spec.EffectiveAuthDatabase())
		assert.Equal(t, "myapp", spec.EffectivePathDatabase())
	})
}

func TestMongoDBUser_ChangedIdentifier(t *testing.T) {
	before := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username: "before-name",
			Database: "before-db",
		},
	}

	after := MongoDBUser{
		Spec: MongoDBUserSpec{
			Username: "after-name",
			Database: "after-db",
		},
		Status: MongoDBUserStatus{
			Username: "before-name",
			Database: "before-db",
		},
	}

	assert.False(t, before.ChangedIdentifier(), "Status has not be initialized yet so the identifier should not have changed")
	assert.True(t, after.ChangedIdentifier(), "Status differs from Spec, so identifier should have changed")

	before = MongoDBUser{
		Spec: MongoDBUserSpec{
			Username: "before-name",
			Database: "before-db",
		},
		Status: MongoDBUserStatus{
			Username: "before-name",
			Database: "before-db",
		},
	}
	assert.False(t, before.ChangedIdentifier(), "Identifier before and after are the same, identifier should not have changed")
}
