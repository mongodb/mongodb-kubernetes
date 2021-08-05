package process

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/wiredtiger"
	appsv1 "k8s.io/api/apps/v1"
)

// CreateMongodProcesses builds the slice of processes based on 'StatefulSet' and 'MongoDB' spec.
// Note, that it's not applicable for sharded cluster processes as each of them may have their own mongod
// options configuration, also mongos process is different.
func CreateMongodProcesses(set appsv1.StatefulSet, containerName string, dbSpec mdbv1.DbSpec) []om.Process {
	return CreateMongodProcessesWithLimit(set, containerName, dbSpec, int(*set.Spec.Replicas))
}

func CreateMongodProcessesWithLimit(set appsv1.StatefulSet, containerName string, dbSpec mdbv1.DbSpec, limit int) []om.Process {
	hostnames, names := util.GetDnsForStatefulSetReplicasSpecified(set, dbSpec.GetClusterDomain(), limit)
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := wiredtiger.CalculateCache(set, containerName, dbSpec.GetMongoDBVersion())

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, dbSpec.GetAdditionalMongodConfig(), dbSpec)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}

// CreateMongodProcessesWithLimitMulti creates the process array for automationConfig based on MultiCluster CR spec
func CreateMongodProcessesWithLimitMulti(mrs mdbmultiv1.MongoDBMulti) []om.Process {
	hostnames := mrs.GetMultiClusterAgentHostnames()
	processes := make([]om.Process, len(hostnames))

	for idx := range hostnames {
		processes[idx] = om.NewMongodProcess(fmt.Sprintf("%s-%d", mrs.Name, idx), hostnames[idx], mrs.Spec.AdditionalMongodConfig, &mrs.Spec)
	}

	return processes
}

func CreateAppDBProcesses(set appsv1.StatefulSet, mongoType om.MongoType,
	mdb omv1.AppDBSpec) []om.Process {

	hostnames, names := util.GetDnsForStatefulSet(set, mdb.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := wiredtiger.CalculateCache(set, util.AppDbContainerName, mdb.GetMongoDBVersion())

	if mongoType != om.ProcessTypeMongod {
		panic("Dev error: Wrong process type passed!")
	}

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcessAppDB(names[idx], hostname, &mdb)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}
