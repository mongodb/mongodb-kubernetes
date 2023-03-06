package memberwatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"time"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/failedcluster"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type MemberClusterHealthChecker struct {
	Cache map[string]*MemberHeathCheck
}

// WatchMemberClusterHealth watches member clusters healthcheck. If a cluster fails healthcheck it re-enqueues the
// MongoDBMultiCluster resources. It is spun up in the mongodb multi reconciler as a go-routine, and is executed every 10 seconds.
func (m *MemberClusterHealthChecker) WatchMemberClusterHealth(log *zap.SugaredLogger, watchChannel chan event.GenericEvent, centralClient kubernetesClient.Client, clustersMap map[string]cluster.Cluster) {

	// check if the local cache is populated if not let's do that
	if len(m.Cache) == 0 {
		// load the kubeconfig file contents from disk
		kubeConfigFile, err := multicluster.NewKubeConfigFile()
		if err != nil {
			log.Errorf("Failed to read KubeConfig file err: %w", err)
			// we can't populate the client so just bail out here
			return
		}

		kubeConfig, err := kubeConfigFile.LoadKubeConfigFile()
		if err != nil {
			log.Errorf("Failed to load the kubeconfig file content err: %w", err)
			return
		}

		for n := range kubeConfig.Contexts {

			clusterName := kubeConfig.Contexts[n].Name
			if _, ok := clustersMap[clusterName]; !ok {
				continue
			}

			server := kubeConfig.Clusters[n].Cluster.Server
			certificateAuthority, err := base64.StdEncoding.DecodeString(kubeConfig.Clusters[n].Cluster.CertificateAuthority)
			if err != nil {
				log.Errorf("Failed to decode certificate for cluster: %s, err: %w", clusterName, err)
				continue
			}

			token := kubeConfig.Users[n].User.Token

			m.Cache[clusterName] = NewMemberHealthCheck(server, certificateAuthority, token, log)

		}
	}

	for {
		log.Info("Running member cluster healthcheck")
		mdbmList := &mdbmulti.MongoDBMultiClusterList{}

		err := centralClient.List(context.TODO(), mdbmList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("Failed to fetch MongoDBMultiClusterList from Kubernetes : %s", err)
		}

		// check the cluster health status corresponding to each member cluster
		for k, v := range m.Cache {
			if v.IsClusterHealthy(log) {
				log.Infof("Cluster %s reported healthy", k)
				continue
			}

			log.Warnf("Cluster %s reported unhealthy", k)
			// re-enqueue all the MDBMultis the operator is watching into the reconcile loop
			for _, mdbm := range mdbmList.Items {

				if shouldAddFailedClusterAnnotation(mdbm.Annotations, k) && multicluster.ShouldPerformFailover() {
					log.Infof("Enqueuing resource: %s, because cluster %s has failed healthcheck", mdbm.Name, k)
					err := AddFailoverAnnotation(mdbm, k, centralClient)
					if err != nil {
						log.Errorf("Failed to add failover annotation to the mdbm resource: %s, error: %s", mdbm.Name, err)
					}
					watchChannel <- event.GenericEvent{Object: &mdbm}
				} else if shouldAddFailedClusterAnnotation(mdbm.Annotations, k) {
					log.Infof("Marking resource: %s, with failed cluster %s annotation", mdbm.Name, k)
					err := addFailedClustersAnnotation(mdbm, k, centralClient)
					if err != nil {
						log.Errorf("Failed to add failed cluster annotation to the mdbm resource: %s, error: %s", mdbm.Name, err)
					}
				}
			}
		}
		time.Sleep(10 * time.Second)
	}

}

// shouldAddFailedClusterAnnotation checks if we should add this cluster in the failedCluster annotation,
// if it's already not present.
func shouldAddFailedClusterAnnotation(annotations map[string]string, clusterName string) bool {
	failedclusters := readFailedClusterAnnotation(annotations)
	if failedclusters == nil {
		return true
	}

	for _, c := range failedclusters {
		if c.ClusterName == clusterName {
			return false
		}
	}
	return true
}

// readFailedClusterAnnotation reads the current failed clusters from the annotation.
func readFailedClusterAnnotation(annotations map[string]string) []failedcluster.FailedCluster {
	if val, ok := annotations[failedcluster.FailedClusterAnnotation]; ok {
		var failedClusters []failedcluster.FailedCluster

		err := json.Unmarshal([]byte(val), &failedClusters)
		if err != nil {
			return nil
		}

		return failedClusters
	}
	return nil
}

// clusterWithMinimumMembers returns the index of the cluster with the minimum number of nodes.
func clusterWithMinimumMembers(clusters []mdbmulti.ClusterSpecItem) int {
	mini, index := math.MaxInt64, -1

	for nn, c := range clusters {
		if c.Members < mini {
			mini = c.Members
			index = nn
		}
	}
	return index
}

// distributeFailedMembers evenly distributes the failed cluster's members amongst the remaining healthy clusters.
func distributeFailedMembers(clusters []mdbmulti.ClusterSpecItem, clustername string) []mdbmulti.ClusterSpecItem {
	// add the cluster override annotations. Get the current clusterspec list from the CR and
	// increase the members of the first cluster by the number of failed nodes
	membersToFailOver := 0

	for n, c := range clusters {
		if c.ClusterName == clustername {
			membersToFailOver = c.Members
			clusters = append(clusters[:n], clusters[n+1:]...)
			break
		}

	}

	for membersToFailOver > 0 {
		// pick the cluster with the minumum number of nodes currently and increament
		// its count by 1.
		nn := clusterWithMinimumMembers(clusters)
		clusters[nn].Members += 1
		membersToFailOver -= 1
	}

	return clusters
}

// AddFailoverAnnotation adds the failed cluster spec to the annotation of the MongoDBMultiCluster CR for it to be used
// while performing the reconcilliation
func AddFailoverAnnotation(mrs mdbmulti.MongoDBMultiCluster, clustername string, client kubernetesClient.Client) error {
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	addFailedClustersAnnotation(mrs, clustername, client)

	currentClusterSpecs := mrs.Spec.ClusterSpecList
	currentClusterSpecs = distributeFailedMembers(currentClusterSpecs, clustername)

	updatedClusterSpec, err := json.Marshal(currentClusterSpecs)
	if err != nil {
		return err
	}

	return annotations.SetAnnotations(mrs.DeepCopy(), map[string]string{failedcluster.ClusterSpecOverrideAnnotation: string(updatedClusterSpec)}, client)

}

func addFailedClustersAnnotation(mrs mdbmulti.MongoDBMultiCluster, clustername string, client kubernetesClient.Client) error {
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	// read the existing failed cliuster annotations
	var clusterData []failedcluster.FailedCluster
	failedclusters := readFailedClusterAnnotation(mrs.Annotations)
	if failedclusters != nil {
		clusterData = failedclusters
	}

	clusterData = append(clusterData, failedcluster.FailedCluster{ClusterName: clustername,
		Members: getClusterMembers(mrs.Spec.ClusterSpecList, clustername)})

	clusterDataBytes, err := json.Marshal(clusterData)
	if err != nil {
		return err
	}
	return annotations.SetAnnotations(mrs.DeepCopy(), map[string]string{failedcluster.FailedClusterAnnotation: string(clusterDataBytes)}, client)
}

func getClusterMembers(clusterSpecList []mdbmulti.ClusterSpecItem, clusterName string) int {
	for _, e := range clusterSpecList {
		if e.ClusterName == clusterName {
			return e.Members
		}
	}
	return 0
}
