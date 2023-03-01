package process

import (
	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
)

func CreateMongodProcessesWithLimit(set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, limit int) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSetReplicasSpecified(set, dbSpec.GetClusterDomain(), limit, dbSpec.GetExternalDomain())
	processes := make([]om.Process, len(hostnames))

	certificateFileName := ""
	if certificateHash, ok := set.Annotations["certHash"]; ok {
		certificateFileName = fmt.Sprintf("%s/%s", util.TLSCertMountPath, certificateHash)
	}

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(idx, names[idx], hostname, dbSpec.GetAdditionalMongodConfig(), dbSpec, certificateFileName)
	}

	return processes
}

// CreateMongodProcessesWithLimitMulti creates the process array for automationConfig based on MultiCluster CR spec
func CreateMongodProcessesWithLimitMulti(mrs mdbmultiv1.MongoDBMultiCluster, certFileName string) ([]om.Process, error) {
	hostnames := make([]string, 0)
	clusterNums := make([]int, 0)
	podNum := make([]int, 0)
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	for _, spec := range clusterSpecList {
		agentHostNames := dns.GetMultiClusterAgentHostnames(mrs.Name, mrs.Namespace, mrs.ClusterNum(spec.ClusterName), spec.Members, spec.ExternalAccessConfiguration.ExternalDomain)
		hostnames = append(hostnames, agentHostNames...)
		for i := 0; i < len(agentHostNames); i++ {
			clusterNums = append(clusterNums, mrs.ClusterNum(spec.ClusterName))
			podNum = append(podNum, i)
		}
	}

	processes := make([]om.Process, len(hostnames))
	for idx := range hostnames {
		processes[idx] = om.NewMongodProcess(idx, fmt.Sprintf("%s-%d-%d", mrs.Name, clusterNums[idx], podNum[idx]), hostnames[idx], mrs.Spec.GetAdditionalMongodConfig(), &mrs.Spec, certFileName)
	}

	return processes, nil
}

func CreateAppDBProcesses(set appsv1.StatefulSet, mongoType om.MongoType,
	mdb omv1.AppDBSpec) []om.Process {

	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.GetClusterDomain(), nil)
	processes := make([]om.Process, len(hostnames))

	if mongoType != om.ProcessTypeMongod {
		panic("Dev error: Wrong process type passed!")
	}

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcessAppDB(names[idx], hostname, &mdb)
	}

	return processes
}
