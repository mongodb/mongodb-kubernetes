package replicaset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

// This test focuses on the integration/glue logic, not re-testing components.
func TestBuildFromMongoDBWithReplicas(t *testing.T) {
	memberOptions := []automationconfig.MemberOptions{
		{Votes: ptr.To(1), Priority: ptr.To("1.0")},
		{Votes: ptr.To(1), Priority: ptr.To("0.5")},
		{Votes: ptr.To(0), Priority: ptr.To("0")}, // Non-voting member
	}

	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rs",
			Namespace: "test-namespace",
		},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version: "7.0.5",
				Security: &mdbv1.Security{
					TLSConfig:      &mdbv1.TLSConfig{},
					Authentication: &mdbv1.Authentication{},
				},
				Connectivity: &mdbv1.MongoDBConnectivity{
					ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{},
				},
			},
			Members:      5, // Spec (target) is 5 members
			MemberConfig: memberOptions,
		},
	}

	// 3 replicas is less than spec.Members, scale up scenario
	replicas := 3
	rsWithProcesses := BuildFromMongoDBWithReplicas(
		"mongodb/mongodb-enterprise-server:7.0.5",
		false,
		mdb,
		replicas,
		"7.0",
		"",
	)

	// Assert: ReplicaSet structure
	assert.Equal(t, "test-rs", rsWithProcesses.Rs.Name(), "ReplicaSet ID should match MongoDB name")
	assert.Equal(t, "1", rsWithProcesses.Rs["protocolVersion"], "Protocol version should be set to 1 for this MongoDB version")

	// Assert: Member count is controlled by replicas parameter, NOT mdb.Spec.Members
	members := rsWithProcesses.Rs["members"].([]om.ReplicaSetMember)
	assert.Len(t, members, replicas, "Member count should match replicas parameter (3), not mdb.Spec.Members (5)")
	assert.Equal(t, 3, len(members), "Should have exactly 3 members")

	// Assert: Processes are created correctly
	assert.Len(t, rsWithProcesses.Processes, replicas, "Process count should match replicas parameter")
	expectedProcessNames := []string{"test-rs-0", "test-rs-1", "test-rs-2"}
	expectedHostnames := []string{
		"test-rs-0.test-rs-svc.test-namespace.svc.cluster.local",
		"test-rs-1.test-rs-svc.test-namespace.svc.cluster.local",
		"test-rs-2.test-rs-svc.test-namespace.svc.cluster.local",
	}

	for i := 0; i < replicas; i++ {
		assert.Equal(t, expectedProcessNames[i], rsWithProcesses.Processes[i].Name(),
			"Process name mismatch at index %d", i)
		assert.Equal(t, expectedHostnames[i], rsWithProcesses.Processes[i].HostName(),
			"Process hostname mismatch at index %d", i)
	}

	// Assert: Member options are propagated
	assert.Equal(t, 1, members[0].Votes(), "Member 0 should have 1 vote")
	assert.Equal(t, float32(1.0), members[0].Priority(), "Member 0 should have priority 1.0")
	assert.Equal(t, 1, members[1].Votes(), "Member 1 should have 1 vote")
	assert.Equal(t, float32(0.5), members[1].Priority(), "Member 1 should have priority 0.5")
	assert.Equal(t, 0, members[2].Votes(), "Member 2 should have 0 votes (non-voting)")
	assert.Equal(t, float32(0), members[2].Priority(), "Member 2 should have priority 0")

	// Assert: Member host field contains process name (not full hostname)
	// Note: ReplicaSetMember["host"] is the process name, not the full hostname
	for i := 0; i < replicas; i++ {
		assert.Equal(t, expectedProcessNames[i], members[i].Name(),
			"Member host should match process name at index %d", i)
	}
}
