package process

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
)

func CreateMongodProcessesWithLimit(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, limit int, fcv string, tlsCertPath string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSetReplicasSpecified(set, dbSpec.GetClusterDomain(), limit, dbSpec.GetExternalDomain())
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, mongoDBImage, forceEnterprise, dbSpec.GetAdditionalMongodConfig(), dbSpec, tlsCertPath, set.Annotations, fcv)
	}

	return processes
}

// CreateMongodProcessesFromMongoDB creates mongod processes directly from MongoDB resource without StatefulSet
func CreateMongodProcessesFromMongoDB(mongoDBImage string, forceEnterprise bool, mdb *mdbv1.MongoDB, limit int, fcv string, tlsCertPath string) []om.Process {
	hostnames, names := dns.GetDNSNames(mdb.Name, mdb.ServiceName(), mdb.Namespace, mdb.Spec.GetClusterDomain(), limit, mdb.Spec.DbCommonSpec.GetExternalDomain())
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, mongoDBImage, forceEnterprise, mdb.Spec.GetAdditionalMongodConfig(), &mdb.Spec, tlsCertPath, mdb.Annotations, fcv)
	}

	return processes
}

// CreateMongodProcessesWithLimitMulti creates the process array for automationConfig based on MultiCluster CR spec
func CreateMongodProcessesWithLimitMulti(mongoDBImage string, forceEnterprise bool, mrs mdbmultiv1.MongoDBMultiCluster, certFileName string) ([]om.Process, error) {
	hostnames := make([]string, 0)
	clusterNums := make([]int, 0)
	podNum := make([]int, 0)
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	for _, spec := range clusterSpecList {
		agentHostNames := dns.GetMultiClusterProcessHostnames(mrs.Name, mrs.Namespace, mrs.ClusterNum(spec.ClusterName), spec.Members, mrs.Spec.GetClusterDomain(), mrs.Spec.GetExternalDomainForMemberCluster(spec.ClusterName))
		hostnames = append(hostnames, agentHostNames...)
		for i := 0; i < len(agentHostNames); i++ {
			clusterNums = append(clusterNums, mrs.ClusterNum(spec.ClusterName))
			podNum = append(podNum, i)
		}
	}

	processes := make([]om.Process, len(hostnames))
	for idx := range hostnames {
		processes[idx] = om.NewMongodProcess(fmt.Sprintf("%s-%d-%d", mrs.Name, clusterNums[idx], podNum[idx]), hostnames[idx], mongoDBImage, forceEnterprise, mrs.Spec.GetAdditionalMongodConfig(), &mrs.Spec, certFileName, mrs.Annotations, mrs.CalculateFeatureCompatibilityVersion())
	}

	return processes, nil
}
