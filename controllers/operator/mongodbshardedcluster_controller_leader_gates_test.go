package operator

// F12c — leader-gate tests for the remaining OM-write sites:
//
//   * agents.UpgradeAllIfNeeded (top of Reconcile)
//   * commonController.ensureRoles (doShardedClusterProcessing)
//   * host.CalculateDiffAndStopMonitoring (updateOmDeploymentShardedCluster)
//   * project.ReadOrCreateProject create-path (via prepareOpsManagerConnectionGated)
//
// Style mirrors mongodbshardedcluster_controller_distributed_test.go: the
// fakeCoordinator from that file is reused. Tests directly call the helper
// method or wrapper; they assert that follower coordinators short-circuit
// the OM-write path while leaders proceed.

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
)

// TestF12c_PrepareConnectionGated_LeaderUsesReadOrCreate verifies the
// leader (and non-distributed mode) routes through the read-or-create path.
// The fake OM in the test scaffolding always has a project, so both leader
// and non-distributed mode succeed and return a connection.
func TestF12c_PrepareConnectionGated_LeaderUsesReadOrCreate(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)

	// Non-distributed mode: nil coordinator.
	conn, _, err := helper.prepareOpsManagerConnectionGated(ctx,
		mdbv1.ProjectConfig{ProjectName: om.TestGroupName, BaseURL: "http://example.com"},
		mdbv1.Credentials{PublicAPIKey: "u", PrivateAPIKey: "k"}, zap.S())
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Distributed-mode leader: same outcome as non-distributed.
	leader := newFakeCoordinator("member-cluster-1", true)
	helper.SetCoordinator(leader)
	conn2, _, err := helper.prepareOpsManagerConnectionGated(ctx,
		mdbv1.ProjectConfig{ProjectName: om.TestGroupName, BaseURL: "http://example.com"},
		mdbv1.Credentials{PublicAPIKey: "u", PrivateAPIKey: "k"}, zap.S())
	require.NoError(t, err)
	require.NotNil(t, conn2)
}

// TestF12c_PrepareConnectionGated_FollowerUsesReadOnly verifies that the
// follower path calls project.ReadProject (which never creates) and surfaces
// ErrProjectNotFound when the project is absent. We use a connection factory
// that simulates "project absent" by returning a mock whose project lookup
// returns nothing — i.e. ReadProject will return ErrProjectNotFound.
//
// In the simpler form below we instead use the existing OM mock which has
// the project pre-populated: the follower succeeds without creating
// anything. We assert that EnsureTagAdded / agent-key writes did NOT happen,
// which is the differentiating behaviour from the leader path.
func TestF12c_PrepareConnectionGated_FollowerSkipsWritePath(t *testing.T) {
	ctx := context.Background()
	helper, _, mockOM := buildMultiClusterShardedHelperForDistributedTest(t)

	follower := newFakeCoordinator("member-cluster-2", false)
	helper.SetCoordinator(follower)

	mockOM.CleanHistory()

	conn, _, err := helper.prepareOpsManagerConnectionGated(ctx,
		mdbv1.ProjectConfig{ProjectName: om.TestGroupName, BaseURL: "http://example.com"},
		mdbv1.Credentials{PublicAPIKey: "u", PrivateAPIKey: "k"}, zap.S())
	require.NoError(t, err)
	require.NotNil(t, conn)

	// On the follower path we did NOT call UpdateProject (which the leader
	// invokes via EnsureTagAdded) and did NOT call CreateProject.
	mockOM.CheckOperationsDidntHappen(t,
		reflect.ValueOf(mockOM.UpdateProject),
		reflect.ValueOf(mockOM.CreateProject),
	)
}

// TestF12c_PrepareConnectionGated_FollowerSurfacesProjectNotFound: when the
// project is genuinely absent, the follower path returns ErrProjectNotFound
// (wrapped or unwrapped). We simulate "project absent" by giving the OM
// mock a project name that doesn't match the one we ask for.
func TestF12c_PrepareConnectionGated_FollowerSurfacesProjectNotFound(t *testing.T) {
	ctx := context.Background()
	helper, _, _ := buildMultiClusterShardedHelperForDistributedTest(t)
	follower := newFakeCoordinator("member-cluster-2", false)
	helper.SetCoordinator(follower)

	// Ask for a project that doesn't exist — the mock OM treats unknown
	// project names as missing.
	_, _, err := helper.prepareOpsManagerConnectionGated(ctx,
		mdbv1.ProjectConfig{ProjectName: "does-not-exist-project-name", BaseURL: "http://example.com"},
		mdbv1.Credentials{PublicAPIKey: "u", PrivateAPIKey: "k"}, zap.S())
	require.Error(t, err)
	assert.True(t, xerrors.Is(err, project.ErrProjectNotFound),
		"expected ErrProjectNotFound, got: %v", err)
}
