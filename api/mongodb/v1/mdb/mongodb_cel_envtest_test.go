package mdb_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodb.yaml")))
}

func newMongoDB(t *testing.T, mutate func(*mdbv1.MongoDB)) *mdbv1.MongoDB {
	t.Helper()
	rs := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      "6.0.0",
				ResourceType: mdbv1.ReplicaSet,
				ConnectionSpec: mdbv1.ConnectionSpec{
					Credentials: "my-creds",
				},
			},
			Members: 3,
		},
	}
	if mutate != nil {
		mutate(rs)
	}
	return rs
}

func withAppDBSecurity(rs *mdbv1.MongoDB) {
	rs.Spec.Security = &mdbv1.Security{
		Authentication: &mdbv1.Authentication{
			Enabled:            true,
			Modes:              []mdbv1.AuthMode{mdbv1.AuthMode("SCRAM")},
			IgnoreUnknownUsers: true,
		},
	}
}

// externalMembersForCEL builds n external mongod members with distinct process names.
func externalMembersForCEL(n int) []mdbv1.ExternalMember {
	members := make([]mdbv1.ExternalMember, n)
	for i := range members {
		members[i] = mdbv1.ExternalMember{
			ProcessName: fmt.Sprintf("ext-%d", i),
			Hostname:    fmt.Sprintf("ext-%d:27017", i),
			Type:        "mongod",
		}
	}
	return members
}

// externalMongosForCEL returns n mongos entries, which the prune-rate rule does not count:
// mongos routers are not replica set members, so removing several at once cannot cost a majority.
func externalMongosForCEL(n int) []mdbv1.ExternalMember {
	members := make([]mdbv1.ExternalMember, n)
	for i := range members {
		members[i] = mdbv1.ExternalMember{
			ProcessName: fmt.Sprintf("mongos-%d", i),
			Hostname:    fmt.Sprintf("mongos-%d:27017", i),
			Type:        "mongos",
		}
	}
	return members
}

func TestMongoDBCELValidation_AppDBRole(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	tests := []struct {
		name          string
		mutate        func(*mdbv1.MongoDB)
		errorContains string
	}{
		{
			name: "role not set is accepted",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = ""
			},
		},
		{
			name: "role AppDB with everything satisfied is accepted",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				withAppDBSecurity(rs)
			},
		},
		{
			name: "role AppDB without explicit authentication is accepted",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
			},
		},
		{
			name: "role AppDB missing SCRAM is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Security = &mdbv1.Security{
					Authentication: &mdbv1.Authentication{
						Enabled:            true,
						Modes:              []mdbv1.AuthMode{mdbv1.AuthMode("MONGODB-CR")},
						IgnoreUnknownUsers: true,
					},
				}
			},
			errorContains: "spec.security.authentication must be enabled with modes [SCRAM] only when spec.role is AppDB",
		},
		{
			name: "role AppDB with SCRAM and X509 modes is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Security = &mdbv1.Security{
					Authentication: &mdbv1.Authentication{
						Enabled:            true,
						Modes:              []mdbv1.AuthMode{mdbv1.AuthMode("SCRAM"), mdbv1.AuthMode("X509")},
						IgnoreUnknownUsers: true,
					},
				}
			},
			errorContains: "spec.security.authentication must be enabled with modes [SCRAM] only when spec.role is AppDB",
		},
		{
			name: "role AppDB with authentication disabled is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Security = &mdbv1.Security{
					Authentication: &mdbv1.Authentication{
						Enabled:            false,
						Modes:              []mdbv1.AuthMode{mdbv1.AuthMode("SCRAM")},
						IgnoreUnknownUsers: true,
					},
				}
			},
			errorContains: "spec.security.authentication must be enabled with modes [SCRAM] only when spec.role is AppDB",
		},
		{
			name: "role AppDB missing ignoreUnknownUsers is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Security = &mdbv1.Security{
					Authentication: &mdbv1.Authentication{
						Enabled:            true,
						Modes:              []mdbv1.AuthMode{mdbv1.AuthMode("SCRAM")},
						IgnoreUnknownUsers: false,
					},
				}
			},
			errorContains: "spec.security.authentication.ignoreUnknownUsers must be true when spec.role is AppDB and authentication is set",
		},
		{
			name: "role AppDB with fewer than 3 members is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Members = 2
				withAppDBSecurity(rs)
			},
			errorContains: "spec.members must be >= 3 when spec.role is AppDB",
		},
		{
			name: "role AppDB with resourceType Standalone is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.ResourceType = mdbv1.Standalone
				withAppDBSecurity(rs)
			},
			errorContains: "spec.resourceType must be ReplicaSet when spec.role is AppDB",
		},
		{
			name: "role AppDB with topology MultiCluster is rejected",
			mutate: func(rs *mdbv1.MongoDB) {
				rs.Spec.Role = mdbv1.RoleAppDB
				rs.Spec.Topology = mdbv1.ClusterTopologyMultiCluster
				withAppDBSecurity(rs)
			},
			errorContains: "spec.topology MultiCluster is not supported when spec.role is AppDB",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := newMongoDB(t, tc.mutate)
			err := k8sClient.Create(ctx, rs)
			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

func TestMongoDBCELValidation_RoleImmutable(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	t.Run("removing role AppDB is rejected", func(t *testing.T) {
		rs := newMongoDB(t, func(rs *mdbv1.MongoDB) {
			rs.Spec.Role = mdbv1.RoleAppDB
			withAppDBSecurity(rs)
		})
		require.NoError(t, k8sClient.Create(ctx, rs))

		rs.Spec.Role = ""
		err := k8sClient.Update(ctx, rs)
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "spec.role is immutable")
	})

	t.Run("adding role AppDB is rejected", func(t *testing.T) {
		rs := newMongoDB(t, nil)
		require.NoError(t, k8sClient.Create(ctx, rs))

		rs.Spec.Role = mdbv1.RoleAppDB
		withAppDBSecurity(rs)
		err := k8sClient.Update(ctx, rs)
		require.Error(t, err)
		assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
		assert.Contains(t, err.Error(), "spec.role is immutable")
	})

	t.Run("unchanged role AppDB is accepted", func(t *testing.T) {
		rs := newMongoDB(t, func(rs *mdbv1.MongoDB) {
			rs.Spec.Role = mdbv1.RoleAppDB
			withAppDBSecurity(rs)
		})
		require.NoError(t, k8sClient.Create(ctx, rs))

		err := k8sClient.Update(ctx, rs)
		require.NoError(t, err)
	})
}

// TestExternalMembersPruneRateCELValidation proves the CEL transition rule on spec is
// enforced by a real API server. Plain Go unit tests cannot exercise oldSelf rules.
func TestExternalMembersPruneRateCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	const errMsg = "at most one external mongod may be removed per update"

	tests := []struct {
		name string
		// createCount is how many external members the resource is created with.
		createCount int
		// updateCount is how many it is updated to. -1 means remove the field entirely.
		updateCount int
		// errorContains is the expected CEL message; empty means the update must succeed.
		errorContains string
	}{
		{name: "removing one member is allowed", createCount: 4, updateCount: 3},
		{name: "removing none is allowed", createCount: 4, updateCount: 4},
		{name: "removing two is rejected", createCount: 4, updateCount: 2, errorContains: errMsg},
		{name: "removing four is rejected", createCount: 4, updateCount: 0, errorContains: errMsg},
		{name: "removing the last one is allowed", createCount: 1, updateCount: 0},
		{name: "clearing the field from one member is allowed", createCount: 1, updateCount: -1},
		{name: "clearing the field from three members is rejected", createCount: 3, updateCount: -1, errorContains: errMsg},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := newMongoDB(t, func(rs *mdbv1.MongoDB) {
				rs.Spec.ExternalMembers = externalMembersForCEL(tc.createCount)
			})
			require.NoError(t, k8sClient.Create(ctx, rs), "create must succeed; the rule is a transition rule")

			if tc.updateCount < 0 {
				rs.Spec.ExternalMembers = nil
			} else {
				rs.Spec.ExternalMembers = externalMembersForCEL(tc.updateCount)
			}
			err := k8sClient.Update(ctx, rs)

			if tc.errorContains == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got %T: %v", err, err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

// TestExternalMongosPruneRateCELValidation proves mongos entries are exempt from the prune-rate
// rule: they hold no votes, so removing all of them in one update cannot break a majority.
func TestExternalMongosPruneRateCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	t.Run("removing every mongos at once is allowed", func(t *testing.T) {
		rs := newMongoDB(t, func(rs *mdbv1.MongoDB) {
			rs.Spec.ExternalMembers = externalMongosForCEL(3)
		})
		require.NoError(t, k8sClient.Create(ctx, rs))

		rs.Spec.ExternalMembers = nil
		assert.NoError(t, k8sClient.Update(ctx, rs))
	})

	t.Run("mongos removals do not mask a mongod removal", func(t *testing.T) {
		rs := newMongoDB(t, func(rs *mdbv1.MongoDB) {
			rs.Spec.ExternalMembers = append(externalMembersForCEL(3), externalMongosForCEL(2)...)
		})
		require.NoError(t, k8sClient.Create(ctx, rs))

		// Drops both mongos (allowed) and two of the three mongods (rejected).
		rs.Spec.ExternalMembers = externalMembersForCEL(1)
		err := k8sClient.Update(ctx, rs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most one external mongod may be removed per update")
	})
}
