package mdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNamedShards_MutuallyExclusiveWithShardCount verifies the webhook rejects
// both spec.shardCount and spec.shards being non-zero simultaneously and
// accepts each used alone.
func TestNamedShards_MutuallyExclusiveWithShardCount(t *testing.T) {
	t.Run("neither set", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().SetShardCountSpec(0).Build()
		_, err := validator.ValidateCreate(ctx, sc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one of spec.shardCount or spec.shards must be specified")
	})

	t.Run("both set", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().
			SetShardCountSpec(3).
			Build()
		sc.Spec.Shards = []Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
			{ShardName: "test-mdb-2"},
		}
		_, err := validator.ValidateCreate(ctx, sc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("only shards set", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().
			SetShardsSpec([]Shard{
				{ShardName: "test-mdb-0"},
				{ShardName: "test-mdb-1"},
			}).
			Build()
		_, err := validator.ValidateCreate(ctx, sc)
		require.NoError(t, err)
	})

	t.Run("only shardCount set", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().Build()
		_, err := validator.ValidateCreate(ctx, sc)
		require.NoError(t, err)
	})
}

func TestNamedShards_InvalidShardName(t *testing.T) {
	cases := []struct {
		name      string
		shardName string
		wantErr   string
	}{
		{"uppercase", "Test-mdb-0", "is not a valid DNS-1123 label"},
		{"underscore", "test-mdb_0", "is not a valid DNS-1123 label"},
		{"leading dash", "-test-mdb", "is not a valid DNS-1123 label"},
		{"empty", "", "is required"},
		{"too long", "s" + repeat("a", 63), "DNS-1123 label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := NewDefaultShardedClusterBuilder().
				SetShardsSpec([]Shard{{ShardName: tc.shardName}}).
				Build()
			_, err := validator.ValidateCreate(ctx, sc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestNamedShards_DuplicateShardName(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-0"},
		}).
		Build()
	_, err := validator.ValidateCreate(ctx, sc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestNamedShards_DuplicateShardId(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0", ShardId: "same"},
			{ShardName: "test-mdb-1", ShardId: "same"},
		}).
		Build()
	_, err := validator.ValidateCreate(ctx, sc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shardId")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestNamedShards_ShardOverridesRejectsUnknownShardName(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{{ShardName: "test-mdb-0"}}).
		Build()
	sc.Spec.ShardOverrides = []ShardOverride{{ShardNames: []string{"test-mdb-42"}}}
	_, err := validator.ValidateCreate(ctx, sc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not refer to any shard in spec.shards")
}

func TestNamedShards_ShardOverridesAcceptsKnownShardName(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
		}).
		Build()
	sc.Spec.ShardOverrides = []ShardOverride{{ShardNames: []string{"test-mdb-1"}}}
	_, err := validator.ValidateCreate(ctx, sc)
	require.NoError(t, err)
}

func TestNamedShards_IdentityImmutableOnUpdate_ShardIdChange(t *testing.T) {
	oldSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0", ShardId: "old-id"},
			{ShardName: "test-mdb-1"},
		}).
		Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0", ShardId: "new-id"},
			{ShardName: "test-mdb-1"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shardId is immutable")
}

func TestNamedShards_IdentityImmutableOnUpdate_ShardCountToShardsEquivalent(t *testing.T) {
	// This is the core migration test at the validation layer: going from
	// spec.shardCount to spec.shards with identity-preserving names MUST be
	// accepted by the webhook.
	oldSc := NewDefaultShardedClusterBuilder().SetShardCountSpec(3).Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
			{ShardName: "test-mdb-2"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.NoError(t, err)
}

func TestNamedShards_MigrationFromShardCount_RejectsTypo(t *testing.T) {
	// User makes a typo while migrating — webhook must reject the update to
	// avoid rewriting the existing StatefulSet/replica set identity.
	oldSc := NewDefaultShardedClusterBuilder().SetShardCountSpec(3).Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "slaney-zero"}, // typo: should have been "test-mdb-1"
			{ShardName: "test-mdb-2"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must preserve shard identity")
}

func TestNamedShards_MigrationFromShardCount_RejectsReorder(t *testing.T) {
	// Reordering during the initial migration would swap shard identities —
	// the webhook must reject it.
	oldSc := NewDefaultShardedClusterBuilder().SetShardCountSpec(3).Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-1"}, // swapped with -0
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-2"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must preserve shard identity")
}

func TestNamedShards_MigrationFromShardCount_RejectsMismatchedShardId(t *testing.T) {
	oldSc := NewDefaultShardedClusterBuilder().SetShardCountSpec(2).Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0", ShardId: "custom-id"}, // identity rewrite
			{ShardName: "test-mdb-1"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must preserve shard identity")
}

func TestNamedShards_MigrationFromShardCount_AllowsAppendedShards(t *testing.T) {
	// Migration preserves identity for existing shards AND appends new ones —
	// the appended shard can have any valid name.
	oldSc := NewDefaultShardedClusterBuilder().SetShardCountSpec(2).Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
			{ShardName: "extra-freshly-added-shard"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.NoError(t, err)
}

func TestNamedShards_AddShardAtEnd(t *testing.T) {
	oldSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
		}).
		Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
			{ShardName: "test-mdb-2"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.NoError(t, err)
}

func TestNamedShards_RemoveShardAllowed(t *testing.T) {
	oldSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-1"},
			{ShardName: "test-mdb-2"},
		}).
		Build()
	newSc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "test-mdb-0"},
			{ShardName: "test-mdb-2"},
		}).
		Build()
	_, err := validator.ValidateUpdate(ctx, oldSc, newSc)
	require.NoError(t, err)
}

// TestNamedShards_ShardSpecificPodSpecForbiddenWithShards verifies the
// deprecated positional pod-spec field cannot be combined with the named
// shards list.
func TestNamedShards_ShardSpecificPodSpecForbiddenWithShards(t *testing.T) {
	sc := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{{ShardName: "test-mdb-0"}}).
		Build()
	sc.Spec.ShardSpecificPodSpec = []MongoDbPodSpec{{}}
	_, err := validator.ValidateCreate(ctx, sc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.shardSpecificPodSpec")
}

// TestResolvedShards verifies the internal helper collapses both API forms
// consistently.
func TestResolvedShards(t *testing.T) {
	t.Run("shardCount synthesises shards", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().SetShardCountSpec(3).Build()
		sc.Name = "mdbs"
		resolved := sc.ResolvedShards()
		require.Len(t, resolved, 3)
		for i, r := range resolved {
			assert.Equal(t, i, r.ShardIdx)
			assert.Equal(t, sc.ShardRsName(i), r.ShardName)
			assert.Equal(t, r.ShardName, r.ShardId, "shardId defaults to shardName when synthesised")
		}
	})

	t.Run("explicit shards passthrough", func(t *testing.T) {
		sc := NewDefaultShardedClusterBuilder().
			SetShardsSpec([]Shard{
				{ShardName: "alpha"},
				{ShardName: "beta", ShardId: "legacy_beta"},
			}).
			Build()
		resolved := sc.ResolvedShards()
		require.Len(t, resolved, 2)
		assert.Equal(t, "alpha", resolved[0].ShardName)
		assert.Equal(t, "alpha", resolved[0].ShardId)
		assert.Equal(t, "beta", resolved[1].ShardName)
		assert.Equal(t, "legacy_beta", resolved[1].ShardId)
	})
}

// TestNamedShards_IdentityPreservation confirms that for a spec migrated
// from shardCount to shards with the synthesised naming convention,
// ShardRsName(i) returns the same string it did before the migration.
func TestNamedShards_IdentityPreservation(t *testing.T) {
	scOld := NewDefaultShardedClusterBuilder().SetShardCountSpec(3).Build()
	scOld.Name = "mdbs"

	scNew := NewDefaultShardedClusterBuilder().
		SetShardsSpec([]Shard{
			{ShardName: "mdbs-0"},
			{ShardName: "mdbs-1"},
			{ShardName: "mdbs-2"},
		}).
		Build()
	scNew.Name = "mdbs"

	for i := 0; i < 3; i++ {
		assert.Equal(t, scOld.ShardRsName(i), scNew.ShardRsName(i),
			"ShardRsName must be stable across shardCount->shards migration")
	}

	assert.Equal(t, scOld.ShardRsNames(), scNew.ShardRsNames())
	assert.Equal(t, scOld.MultiShardRsName(0, 1), scNew.MultiShardRsName(0, 1))
}

// repeat is a tiny helper to avoid importing strings just for one test input.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
