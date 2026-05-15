package coordraft

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
)

// TestPublishAgentKey_FSMDistribution — the leader publishes the OM agent
// API key; the followers see it on their local FSMs without making an OM
// read. This avoids relying on omProject.AgentAPIKey coming back through
// the project-read path, which was the previous Phase D follower behaviour.
func TestPublishAgentKey_FSMDistribution(t *testing.T) {
	_, coords, leaderIdx := newCoordinatorClusterForTest(t, 3)
	leader := coords[leaderIdx]

	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}
	const projectID = "abc1234"
	const agentKey = "secret-key-bytes"

	// Initially no FSM entry on any node.
	for i, c := range coords {
		assert.Empty(t, c.GetAgentKey(crKey, projectID), "node %d should have no key initially", i)
	}

	// Leader publishes.
	require.NoError(t, leader.PublishAgentKey(crKey, projectID, agentKey))

	// Wait for raft replication, then assert every node sees the key.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, c := range coords {
			if c.GetAgentKey(crKey, projectID) != agentKey {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i, c := range coords {
		assert.Equalf(t, agentKey, c.GetAgentKey(crKey, projectID), "node %d FSM never observed published key", i)
	}

	// Re-publishing the same key is a no-op (fast-path).
	require.NoError(t, leader.PublishAgentKey(crKey, projectID, agentKey))

	// Different projectIDs are independent.
	require.NoError(t, leader.PublishAgentKey(crKey, "other-project", "different-key"))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if coords[(leaderIdx+1)%3].GetAgentKey(crKey, "other-project") == "different-key" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	follower := coords[(leaderIdx+1)%3]
	assert.Equal(t, agentKey, follower.GetAgentKey(crKey, projectID))
	assert.Equal(t, "different-key", follower.GetAgentKey(crKey, "other-project"))

	// Different CRs are independent.
	otherCR := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "other-cr"}
	assert.Empty(t, follower.GetAgentKey(otherCR, projectID))
}

func TestPublishAgentKey_ValidatesInput(t *testing.T) {
	_, coords, leaderIdx := newCoordinatorClusterForTest(t, 1)
	leader := coords[leaderIdx]
	crKey := coordination.CRKey{Kind: "MongoDB", Namespace: "ns", Name: "cr"}

	assert.Error(t, leader.PublishAgentKey(crKey, "", "key"))
	assert.Error(t, leader.PublishAgentKey(crKey, "proj", ""))
	assert.NoError(t, leader.PublishAgentKey(crKey, "proj", "key"))
}
