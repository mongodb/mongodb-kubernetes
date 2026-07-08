package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	ac "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

func TestDetermineNextProcessIdStartingPoint(t *testing.T) {
	t.Run("New id should be higher than any other id", func(t *testing.T) {
		desiredProcesses := []Process{
			{
				"name": "p-0",
			},
			{
				"name": "p-1",
			},
			{
				"name": "p-2",
			},
			{
				"name": "p-3",
			},
		}

		existingIds := map[string]int{
			"p-0": 0,
			"p-1": 1,
			"p-2": 2,
			"p-3": 3,
		}

		assert.Equal(t, 4, determineNextProcessIdStartingPoint(desiredProcesses, existingIds))
	})

	t.Run("New id should be higher than other ids even if there are gaps in between", func(t *testing.T) {
		desiredProcesses := []Process{
			{
				"name": "p-0",
			},
			{
				"name": "p-1",
			},
			{
				"name": "p-2",
			},
		}

		existingIds := map[string]int{
			"p-0": 0,
			"p-1": 5,
			"p-2": 3,
		}

		assert.Equal(t, 6, determineNextProcessIdStartingPoint(desiredProcesses, existingIds))
	})
}

func TestNewMultiClusterReplicaSetWithProcesses(t *testing.T) {
	tests := []struct {
		name          string
		processes     []Process
		memberOptions []ac.MemberOptions
		expected      ReplicaSetWithProcesses
	}{
		{
			name: "Same number of processes and member options",
			processes: []Process{
				{
					"name": "p-0",
				},
				{
					"name": "p-1",
				},
			},
			memberOptions: []ac.MemberOptions{
				{
					Votes:    ptr.To(1),
					Priority: ptr.To("1.3"),
				},
				{
					Votes:    ptr.To(0),
					Priority: ptr.To("0.7"),
				},
			},
			expected: ReplicaSetWithProcesses{
				Rs: ReplicaSet{
					"_id": "mdb-multi", "members": []ReplicaSetMember{
						{"_id": 0, "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						{"_id": 1, "host": "p-1", "priority": float32(0.7), "tags": map[string]string{}, "votes": 0},
					},
					"protocolVersion": "1",
				},
				Processes: []Process{
					{"name": "p-0", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-1", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
				},
			},
		},
		{
			name: "More member options than processes",
			processes: []Process{
				{
					"name": "p-0",
				},
				{
					"name": "p-1",
				},
			},
			memberOptions: []ac.MemberOptions{
				{
					Votes:    ptr.To(1),
					Priority: ptr.To("1.3"),
				},
				{
					Votes:    ptr.To(0),
					Priority: ptr.To("0.7"),
				},
				{
					Votes: ptr.To(1),
					Tags: map[string]string{
						"env": "dev",
					},
				},
			},
			expected: ReplicaSetWithProcesses{
				Rs: ReplicaSet{
					"_id": "mdb-multi", "members": []ReplicaSetMember{
						{"_id": 0, "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						{"_id": 1, "host": "p-1", "priority": float32(0.7), "tags": map[string]string{}, "votes": 0},
					},
					"protocolVersion": "1",
				},
				Processes: []Process{
					{"name": "p-0", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-1", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
				},
			},
		},
		{
			name: "Less member options than processes",
			processes: []Process{
				{
					"name": "p-0",
				},
				{
					"name": "p-1",
				},
			},
			memberOptions: []ac.MemberOptions{
				{
					Votes:    ptr.To(1),
					Priority: ptr.To("1.3"),
				},
			},
			expected: ReplicaSetWithProcesses{
				Rs: ReplicaSet{
					"_id": "mdb-multi", "members": []ReplicaSetMember{
						{"_id": 0, "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						// Defaulting priority 1.0 and votes to 1 when no member options are present
						{"_id": 1, "host": "p-1", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
					},
					"protocolVersion": "1",
				},
				Processes: []Process{
					{"name": "p-0", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-1", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
				},
			},
		},
		{
			name: "No member options",
			processes: []Process{
				{
					"name": "p-0",
				},
				{
					"name": "p-1",
				},
			},
			memberOptions: []ac.MemberOptions{},
			expected: ReplicaSetWithProcesses{
				Rs: ReplicaSet{
					"_id": "mdb-multi", "members": []ReplicaSetMember{
						// Defaulting priority 1.0 and votes to 1 when no member options are present
						{"_id": 0, "host": "p-0", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
						// Defaulting priority 1.0 and votes to 1 when no member options are present
						{"_id": 1, "host": "p-1", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
					},
					"protocolVersion": "1",
				},
				Processes: []Process{
					{"name": "p-0", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-1", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
				},
			},
		},
		{
			name:      "No processes",
			processes: []Process{},
			memberOptions: []ac.MemberOptions{
				{
					Votes:    ptr.To(1),
					Priority: ptr.To("1.3"),
				},
				{
					Votes:    ptr.To(0),
					Priority: ptr.To("0.7"),
				},
			},
			expected: ReplicaSetWithProcesses{
				Rs:        ReplicaSet{"_id": "mdb-multi", "members": []ReplicaSetMember{}, "protocolVersion": "1"},
				Processes: []Process{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := NewMultiClusterReplicaSetWithProcesses(NewReplicaSet("mdb-multi", "5.0.5"), tt.processes, tt.memberOptions, map[string]int{}, nil)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestNewReplicaSetWithProcesses_ShardedClusterPreservesProcessIds(t *testing.T) {
	t.Run("Existing process IDs are preserved for known processes", func(t *testing.T) {
		processes := []Process{
			{"name": "shard-0-0"},
			{"name": "shard-0-1"},
			{"name": "shard-0-2"},
		}
		existingProcessIds := map[string]int{
			"shard-0-0": 10,
			"shard-0-1": 20,
			"shard-0-2": 30,
		}

		actual := NewReplicaSetWithProcesses(NewReplicaSet("shard-0", "7.0.0"), processes, []ac.MemberOptions{}, existingProcessIds)

		members := actual.Rs.Members()
		assert.Len(t, members, 3)
		assert.Equal(t, 10, members[0].Id())
		assert.Equal(t, 20, members[1].Id())
		assert.Equal(t, 30, members[2].Id())
	})

	t.Run("New process added alongside existing ones gets an ID higher than all existing IDs", func(t *testing.T) {
		existingProcessIds := map[string]int{
			"shard-0-0": 10,
			"shard-0-1": 20,
			"shard-0-2": 30,
		}
		// shard-0-3 is a new process not present in existingProcessIds
		processes := []Process{
			{"name": "shard-0-0"},
			{"name": "shard-0-1"},
			{"name": "shard-0-2"},
			{"name": "shard-0-3"},
		}

		actual := NewReplicaSetWithProcesses(NewReplicaSet("shard-0", "7.0.0"), processes, []ac.MemberOptions{}, existingProcessIds)

		members := actual.Rs.Members()
		assert.Len(t, members, 4)
		assert.Equal(t, 10, members[0].Id())
		assert.Equal(t, 20, members[1].Id())
		assert.Equal(t, 30, members[2].Id())
		// The new process must receive an ID strictly greater than the highest existing ID (30).
		assert.Equal(t, 31, members[3].Id())
	})
}
