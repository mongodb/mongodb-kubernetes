package om

import (
	"strconv"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/automationconfig"
	"github.com/stretchr/testify/assert"
)

func makeMinimalRsWithProcesses() ReplicaSetWithProcesses {
	replicaSetWithProcesses := NewReplicaSet("my-test-repl", "4.2.1")
	mdb := mdbv1.MongoDB{Spec: mdbv1.MongoDbSpec{DbCommonSpec: mdbv1.DbCommonSpec{Version: "4.2.1"}}}
	mdb.InitDefaults()
	processes := make([]Process, 3)
	memberOptions := make([]automationconfig.MemberOptions, 3)
	for i := range processes {
		proc := NewMongodProcess("my-test-repl-"+strconv.Itoa(i), "my-test-repl-"+strconv.Itoa(i), &mdbv1.AdditionalMongodConfig{}, &mdb.Spec, "", nil, "")
		processes[i] = proc
		replicaSetWithProcesses.addMember(proc, "", memberOptions[i])
	}
	return NewReplicaSetWithProcesses(replicaSetWithProcesses, processes, memberOptions)
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

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs)
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

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs)
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

	opsManagerRsWithProcesses.Rs.mergeFrom(operatorRsWithProcesses.Rs)
	for i, member := range opsManagerRsWithProcesses.Rs.Members() {
		assert.Equal(t, horizonsNew[i], member.getHorizonConfig())
	}
}
