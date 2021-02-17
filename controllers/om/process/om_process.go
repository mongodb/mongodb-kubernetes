package process

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/wiredtiger"
	appsv1 "k8s.io/api/apps/v1"
)

// CreateMongodProcesses builds the slice of processes based on 'StatefulSet' and 'MongoDB' spec.
// Note, that it's not applicable for sharded cluster processes as each of them may have their own mongod
// options configuration, also mongos process is different
func CreateMongodProcesses(set appsv1.StatefulSet, containerName string, mdb *mdbv1.MongoDB) []om.Process {
	hostnames, names := util.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := wiredtiger.CalculateCache(set, containerName, mdb.Spec.GetVersion())

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, mdb.Spec.AdditionalMongodConfig, mdb)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}

func CreateAppDBProcesses(set appsv1.StatefulSet, mongoType om.MongoType,
	mdb omv1.AppDB) []om.Process {

	hostnames, names := util.GetDnsForStatefulSet(set, mdb.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := wiredtiger.CalculateCache(set, util.AppDbContainerName, mdb.GetVersion())

	if mongoType != om.ProcessTypeMongod {
		panic("Dev error: Wrong process type passed!")
	}

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcessAppDB(names[idx], hostname, mdb)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}
