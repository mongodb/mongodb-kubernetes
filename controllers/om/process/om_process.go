package process

import (
	"fmt"
	"strings"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// CreateMongodProcessesFromMongoDB creates mongod processes directly from MongoDB resource without StatefulSet
func CreateMongodProcessesFromMongoDB(mongoDBImage string, forceEnterprise bool, mdb *mdbv1.MongoDB, limit int, fcv string, tlsCertPath string, defaultArchitecture architectures.DefaultArchitecture, useLegacyNames bool) []om.Process {
	hostnames, names := dns.GetDNSNames(mdb.Name, mdb.ServiceName(), mdb.Namespace, mdb.Spec.GetClusterDomain(), limit, mdb.Spec.DbCommonSpec.GetExternalDomain())
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		name := names[idx]
		if !useLegacyNames {
			name = PodNameToProcessName(name, mdb.Namespace)
		}
		processes[idx] = om.NewMongodProcess(name, hostname, mongoDBImage, forceEnterprise, mdb.Spec.GetAdditionalMongodConfig(), &mdb.Spec, tlsCertPath, mdb.Annotations, fcv, defaultArchitecture)
	}

	return processes
}

// CreateMongodProcessesWithLimitMulti creates the process array for automationConfig based on MultiCluster CR spec
func CreateMongodProcessesWithLimitMulti(mongoDBImage string, forceEnterprise bool, mrs mdbmultiv1.MongoDBMultiCluster, certFileName string, defaultArchitecture architectures.DefaultArchitecture, useLegacyNames bool) ([]om.Process, error) {
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
		for i := range agentHostNames {
			clusterNums = append(clusterNums, mrs.ClusterNum(spec.ClusterName))
			podNum = append(podNum, i)
		}
	}

	processes := make([]om.Process, len(hostnames))
	for idx := range hostnames {
		name := fmt.Sprintf("%s-%d-%d", mrs.Name, clusterNums[idx], podNum[idx])
		if !useLegacyNames {
			name = PodNameToProcessName(name, mrs.Namespace)
		}
		processes[idx] = om.NewMongodProcess(name, hostnames[idx], mongoDBImage, forceEnterprise, mrs.Spec.GetAdditionalMongodConfig(), &mrs.Spec, certFileName, mrs.Annotations, mrs.CalculateFeatureCompatibilityVersion(), defaultArchitecture)
	}

	return processes, nil
}

// PodNameToProcessName returns the process name with the "k8s/<namespace>/" prefix added to the legacy name
func PodNameToProcessName(legacyName, namespace string) string {
	return fmt.Sprintf("k8s/%s/%s", namespace, legacyName)
}

// IsResourceProcess returns true for the names of the processes of the resource, with or without the k8s/<namespace>/ prefix:
// its Pods (<name>-<suffix>) and a process named exactly like the resource.
func IsResourceProcess(processName, resourceName, resourceNamespace string) bool {
	canonicalProcessName := strings.TrimPrefix(processName, PodNameToProcessName("", resourceNamespace))
	rest, ok := strings.CutPrefix(canonicalProcessName, resourceName)
	if ok {
		return rest == "" || (strings.HasPrefix(rest, "-") && len(rest) > 1)
	}

	return false
}
