package om

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

func makeMinimalRsWithProcesses() ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSet("my-test-repl", "4.2.1")
	mdb := mdbv1.MongoDB{Spec: mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "4.2.1"}}}
	mdb.InitDefaults()
	processes := make([]Process, 3)
	memberOptions := make([]automationconfig.MemberOptions, 3)
	for i := range processes {
		proc := NewMongodProcess("my-test-repl-"+strconv.Itoa(i), "my-test-repl-"+strconv.Itoa(i), "fake-mongoDBImage", false, &mdbv1.AdditionalMongodConfig{}, &mdb.Spec, "", nil, "")
		processes[i] = proc
		replicaSetWithProcesses.addMember(proc, nil, memberOptions[i])
	}
	return NewReplicaSetWithProcesses(replicaSetWithProcesses, processes, memberOptions, nil)
}

// TestMergeHorizonsAdd checks that horizon configuration is appropriately
// added.
func TestMergeHorizonsAdd(t *testing.T) {
	opsManagerRsWithProcesses := makeMinimalRsWithProcesses()
	operatorRsWithProcesses := makeMinimalRsWithProcesses()
	horizons := []mdbv1.MongoDBHorizonConfig{
		{"name1": "my-db.my-test.com:12345"},
		{"name1": "my-db.my-test.com:12346"},
		{"name1": "my-db.my-test.com:12347"},
	}
	operatorRsWithProcesses.SetHorizons(horizons)

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs, nil)
	for i, member := range opsManagerRsWithProcesses.Rs.Members() {
		assert.Equal(t, horizons[i], member.getHorizonConfig())
	}
}

// TestMergeHorizonsIgnore checks that old horizon configuration is removed
// when merged with a replica set with no horizon configuration.
func TestMergeHorizonsRemove(t *testing.T) {
	opsManagerRsWithProcesses := makeMinimalRsWithProcesses()
	horizons := []mdbv1.MongoDBHorizonConfig{
		{"name1": "my-db.my-test.com:12345"},
		{"name1": "my-db.my-test.com:12346"},
		{"name1": "my-db.my-test.com:12347"},
	}
	opsManagerRsWithProcesses.SetHorizons(horizons)
	operatorRsWithProcesses := makeMinimalRsWithProcesses()

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs, nil)
	for _, member := range opsManagerRsWithProcesses.Rs.Members() {
		assert.Equal(t, mdbv1.MongoDBHorizonConfig{}, member.getHorizonConfig())
	}
}

// TestMergeHorizonsOverride checks that new horizon configuration overrides
// old horizon configuration.
func TestMergeHorizonsOverride(t *testing.T) {
	opsManagerRsWithProcesses := makeMinimalRsWithProcesses()
	horizonsOld := []mdbv1.MongoDBHorizonConfig{
		{"name1": "my-db.my-test.com:12345"},
		{"name1": "my-db.my-test.com:12346"},
		{"name1": "my-db.my-test.com:12347"},
	}
	horizonsNew := []mdbv1.MongoDBHorizonConfig{
		{"name2": "my-db.my-test.com:12345"},
		{"name2": "my-db.my-test.com:12346"},
		{"name2": "my-db.my-test.com:12347"},
	}
	opsManagerRsWithProcesses.SetHorizons(horizonsOld)
	operatorRsWithProcesses := makeMinimalRsWithProcesses()
	operatorRsWithProcesses.SetHorizons(horizonsNew)

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs, nil)
	for i, member := range opsManagerRsWithProcesses.Rs.Members() {
		assert.Equal(t, horizonsNew[i], member.getHorizonConfig())
	}
}

func TestMergeFrom_ExternalMemberPreserved(t *testing.T) {
	omRsWithProcesses := makeMinimalRsWithProcesses()
	extMember := ReplicaSetMember{}
	extMember["host"] = "external-host:27017"
	extMember["_id"] = 3
	extMember["votes"] = 1
	extMember["priority"] = float32(1)
	extMember["tags"] = map[string]string{}
	omRsWithProcesses.Rs.setMembers(append(omRsWithProcesses.Rs.Members(), extMember))

	operatorRsWithProcesses := makeMinimalRsWithProcesses()

	externalMembers := []string{"external-host:27017"}
	removedMembers := omRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs, externalMembers)

	assert.NotContains(t, removedMembers, "external-host:27017")

	memberHosts := make([]string, len(omRsWithProcesses.Rs.Members()))
	for i, m := range omRsWithProcesses.Rs.Members() {
		memberHosts[i] = m.Name()
	}
	assert.Contains(t, memberHosts, "external-host:27017")
}

func TestMergeFrom_NonExternalExtraMemberRemoved(t *testing.T) {
	omRsWithProcesses := makeMinimalRsWithProcesses()
	staleMember := ReplicaSetMember{}
	staleMember["host"] = "stale-host:27017"
	staleMember["_id"] = 3
	staleMember["votes"] = 1
	staleMember["priority"] = float32(1)
	staleMember["tags"] = map[string]string{}
	omRsWithProcesses.Rs.setMembers(append(omRsWithProcesses.Rs.Members(), staleMember))

	operatorRsWithProcesses := makeMinimalRsWithProcesses()
	removedMembers := omRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs, nil)

	assert.Contains(t, removedMembers, "stale-host:27017")

	// Verify the stale member is actually gone from the RS
	memberHosts := make([]string, len(omRsWithProcesses.Rs.Members()))
	for i, m := range omRsWithProcesses.Rs.Members() {
		memberHosts[i] = m.Name()
	}
	assert.NotContains(t, memberHosts, "stale-host:27017")
}

func TestCountVotingMembers(t *testing.T) {
	mkMember := func(host string, votes int) ReplicaSetMember {
		return ReplicaSetMember{"_id": 0, "host": host, "votes": votes, "priority": float32(1)}
	}

	tests := []struct {
		name          string
		members       []ReplicaSetMember
		externalNames []string
		wantK8s       int
		wantExternal  int
	}{
		{
			name:          "all K8s voting, no externals",
			members:       []ReplicaSetMember{mkMember("rs-0", 1), mkMember("rs-1", 1), mkMember("rs-2", 1)},
			externalNames: nil,
			wantK8s:       3,
			wantExternal:  0,
		},
		{
			name:          "mixed votes, no externals",
			members:       []ReplicaSetMember{mkMember("rs-0", 1), mkMember("rs-1", 0), mkMember("rs-2", 1)},
			externalNames: nil,
			wantK8s:       2,
			wantExternal:  0,
		},
		{
			name: "K8s + voting externals",
			members: []ReplicaSetMember{
				mkMember("rs-0", 1), mkMember("rs-1", 1), mkMember("rs-2", 1),
				mkMember("ext-0", 1), mkMember("ext-1", 1),
			},
			externalNames: []string{"ext-0", "ext-1"},
			wantK8s:       3,
			wantExternal:  2,
		},
		{
			name: "K8s + non-voting externals",
			members: []ReplicaSetMember{
				mkMember("rs-0", 1), mkMember("rs-1", 1), mkMember("rs-2", 1),
				mkMember("ext-0", 0), mkMember("ext-1", 0),
			},
			externalNames: []string{"ext-0", "ext-1"},
			wantK8s:       3,
			wantExternal:  0,
		},
		{
			name: "K8s + mixed voting externals",
			members: []ReplicaSetMember{
				mkMember("rs-0", 1), mkMember("rs-1", 1),
				mkMember("ext-0", 1), mkMember("ext-1", 0), mkMember("ext-2", 1),
			},
			externalNames: []string{"ext-0", "ext-1", "ext-2"},
			wantK8s:       2,
			wantExternal:  2,
		},
		{
			name:          "external name not present in RS — ignored, not counted",
			members:       []ReplicaSetMember{mkMember("rs-0", 1), mkMember("rs-1", 1), mkMember("rs-2", 1)},
			externalNames: []string{"ext-missing"},
			wantK8s:       3,
			wantExternal:  0,
		},
		{
			name:          "empty RS",
			members:       []ReplicaSetMember{},
			externalNames: []string{"ext-0"},
			wantK8s:       0,
			wantExternal:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := ReplicaSet{"_id": "my-rs", "members": tt.members}
			k8sVoting, externalVoting := CountVotingMembers(rs, tt.externalNames)
			assert.Equal(t, tt.wantK8s, k8sVoting, "k8s voting")
			assert.Equal(t, tt.wantExternal, externalVoting, "external voting")
		})
	}
}
