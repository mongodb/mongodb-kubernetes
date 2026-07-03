package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	ac "github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
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
		name               string
		processes          []Process
		memberOptions      []ac.MemberOptions
		existingProcessIds map[string]int
		expected           ReplicaSetWithProcesses
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
						{"_id": "0", "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						{"_id": "1", "host": "p-1", "priority": float32(0.7), "tags": map[string]string{}, "votes": 0},
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
						{"_id": "0", "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						{"_id": "1", "host": "p-1", "priority": float32(0.7), "tags": map[string]string{}, "votes": 0},
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
						{"_id": "0", "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						// Defaulting priority 1.0 and votes to 1 when no member options are present
						{"_id": "1", "host": "p-1", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
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
						{"_id": "0", "host": "p-0", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
						// Defaulting priority 1.0 and votes to 1 when no member options are present
						{"_id": "1", "host": "p-1", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
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
		{
			name: "Existing process ids are preserved and new processes get incrementing ids",
			processes: []Process{
				{
					"name": "p-0",
				},
				{
					"name": "p-1",
				},
				{
					"name": "p-2",
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
					Votes:    ptr.To(1),
					Priority: ptr.To("1.0"),
				},
			},
			// simulates e.g. switching the OpsManager project: p-0 and p-1 already existed
			// (e.g. with ids from a previous project) and must keep their ids, while the new
			// process p-2 must get a fresh, non-overlapping id.
			existingProcessIds: map[string]int{
				"p-0": 5,
				"p-1": 7,
			},
			expected: ReplicaSetWithProcesses{
				Rs: ReplicaSet{
					"_id": "mdb-multi", "members": []ReplicaSetMember{
						{"_id": "5", "host": "p-0", "priority": float32(1.3), "tags": map[string]string{}, "votes": 1},
						{"_id": "7", "host": "p-1", "priority": float32(0.7), "tags": map[string]string{}, "votes": 0},
						{"_id": "8", "host": "p-2", "priority": float32(1.0), "tags": map[string]string{}, "votes": 1},
					},
					"protocolVersion": "1",
				},
				Processes: []Process{
					{"name": "p-0", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-1", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
					{"name": "p-2", "args2_6": map[string]interface{}{"replication": map[string]interface{}{"replSetName": "mdb-multi"}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existingProcessIds := tt.existingProcessIds
			if existingProcessIds == nil {
				existingProcessIds = map[string]int{}
			}
			actual := NewMultiClusterReplicaSetWithProcesses(NewReplicaSet("mdb-multi", "5.0.5"), tt.processes, tt.memberOptions, existingProcessIds, nil)
			assert.Equal(t, tt.expected, actual)
		})
	}
}
