package process

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
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
	hostnames, names := dns.GetDnsForStatefulSetReplicasSpecified(set, dbSpec.GetClusterDomain(), limit)
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
func CreateMongodProcessesWithLimitMulti(mrs mdbmultiv1.MongoDBMulti) ([]om.Process, error) {
	hostnames := make([]string, 0)
	clusterNums := make([]int, 0)
	podNum := make([]int, 0)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	for clusterNum, spec := range clusterSpecList {
		agentHostNames := dns.GetMultiClusterAgentHostnames(mrs.Name, mrs.Namespace, clusterNum, spec.Members)
		hostnames = append(hostnames, agentHostNames...)
		for i := 0; i < len(agentHostNames); i++ {
			clusterNums = append(clusterNums, clusterNum)
			podNum = append(podNum, i)
		}
	}

	processes := make([]om.Process, len(hostnames))
	for idx := range hostnames {
		processes[idx] = om.NewMongodProcess(fmt.Sprintf("%s-%d-%d", mrs.Name, clusterNums[idx], podNum[idx]), hostnames[idx], mrs.Spec.AdditionalMongodConfig, &mrs.Spec)
	}

	return processes, nil
}

// CreateMongodProcessesWithLimitPerCluster creates a mapping of cluster name to slice of processes
// corresponding to that cluster.
func CreateMongodProcessesWithLimitPerCluster(mrs mdbmultiv1.MongoDBMulti) (map[string][]om.Process, error) {
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	allProcesses, err := CreateMongodProcessesWithLimitMulti(mrs)
	if err != nil {
		return nil, err
	}

	processMap := map[string][]om.Process{}

	idx := 0
	for _, item := range clusterSpecList {
		var itemProcesses []om.Process
		for i := 0; i < item.Members; i++ {
			itemProcesses = append(itemProcesses, allProcesses[idx])
			idx++
		}
		processMap[item.ClusterName] = itemProcesses
	}
	return processMap, nil
}

func CreateAppDBProcesses(set appsv1.StatefulSet, mongoType om.MongoType,
	mdb omv1.AppDBSpec) []om.Process {

	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.GetClusterDomain())
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
