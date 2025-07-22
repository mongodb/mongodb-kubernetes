package process

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func CreateMongodProcessesWithLimit(mongoDBImage string, set appsv1.StatefulSet, dbSpec mdbv1.DbSpec, limit int, fcv string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSetReplicasSpecified(set, dbSpec.GetClusterDomain(), limit, dbSpec.GetExternalDomain())
	processes := make([]om.Process, len(hostnames))

	certificateFileName := ""
	if certificateHash, ok := set.Annotations[certs.CertHashAnnotationKey]; ok {
		certificateFileName = fmt.Sprintf("%s/%s", util.TLSCertMountPath, certificateHash)
	}

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, mongoDBImage, dbSpec.GetAdditionalMongodConfig(), dbSpec, certificateFileName, set.Annotations, fcv)
	}

	return processes
}

// CreateMongodProcessesWithLimitMulti creates the process array for automationConfig based on MultiCluster CR spec
func CreateMongodProcessesWithLimitMulti(mongoDBImage string, mrs mdbmultiv1.MongoDBMultiCluster, certFileName string) ([]om.Process, error) {
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
		processes[idx] = om.NewMongodProcess(fmt.Sprintf("%s-%d-%d", mrs.Name, clusterNums[idx], podNum[idx]), hostnames[idx], mongoDBImage, mrs.Spec.GetAdditionalMongodConfig(), &mrs.Spec, certFileName, mrs.Annotations, mrs.CalculateFeatureCompatibilityVersion())
	}

	return processes, nil
}
