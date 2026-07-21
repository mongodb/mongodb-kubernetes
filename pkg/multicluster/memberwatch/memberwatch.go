package memberwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"

	restclient "k8s.io/client-go/rest"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster/failedcluster"
)

type MemberClusterHealthChecker struct {
	Cache                 map[string]ClusterHealthChecker
	HealthyStreak         map[string]int
	RequiredHealthyStreak int
	// ClientTimeout is the timeout for the per-cluster health-check HTTP client.
	// When zero, DefaultClientTimeout is used.
	ClientTimeout time.Duration
	mu            sync.RWMutex
}

func (m *MemberClusterHealthChecker) HealthyStreakFor(cluster string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.HealthyStreak[cluster]
}

type ClusterCredentials struct {
	Server               string
	CertificateAuthority []byte
	Token                string
}

// credentialsFromRestConfig extracts the API server URL, CA, and bearer token used by the
// health checker from a member cluster's rest.Config. Falling back to CAFile/BearerTokenFile
// covers configs that reference on-disk material rather than inline data.
func credentialsFromRestConfig(restConfig *restclient.Config) (*ClusterCredentials, error) {
	ca := restConfig.CAData
	if len(ca) == 0 && restConfig.CAFile != "" {
		data, err := os.ReadFile(restConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file %s: %w", restConfig.CAFile, err)
		}
		ca = data
	}

	token := restConfig.BearerToken
	if token == "" && restConfig.BearerTokenFile != "" {
		data, err := os.ReadFile(restConfig.BearerTokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading token file %s: %w", restConfig.BearerTokenFile, err)
		}
		token = string(data)
	}

	return &ClusterCredentials{
		Server:               restConfig.Host,
		CertificateAuthority: ca,
		Token:                token,
	}, nil
}

// populateCache builds a per-cluster health checker for every member cluster in clustersMap,
// sourcing the server URL, CA, and token from each cluster's in-memory rest.Config
// (cluster.GetConfig()). This is independent of how the clusters were discovered — MemberCluster
// CRs or the legacy mounted kubeconfig — since both populate clustersMap identically.
func (m *MemberClusterHealthChecker) populateCache(clustersMap map[string]cluster.Cluster, log *zap.SugaredLogger) {
	timeout := m.ClientTimeout
	if timeout <= 0 {
		timeout = DefaultClientTimeout
	}

	for clusterName, memberCluster := range clustersMap {
		restConfig := memberCluster.GetConfig()
		if restConfig == nil {
			log.Errorf("Skipping cluster %s: no REST config available", clusterName)
			continue
		}
		credentials, err := credentialsFromRestConfig(restConfig)
		if err != nil {
			log.Errorf("Skipping cluster %s: %v", clusterName, err)
			continue
		}
		m.Cache[clusterName] = NewMemberHealthCheck(credentials.Server, credentials.CertificateAuthority, credentials.Token, log, WithTimeout(timeout))
		m.HealthyStreak[clusterName] = 0
	}
}

// WatchMemberClusterHealth watches member clusters healthcheck. If a cluster fails healthcheck it re-enqueues the
// MongoDBMultiCluster resources. It is spun up in the mongodb multi reconciler as a go-routine, and is executed every 10 seconds.
func (m *MemberClusterHealthChecker) WatchMemberClusterHealth(ctx context.Context, log *zap.SugaredLogger, watchChannel chan event.GenericEvent, centralClient kubernetesClient.Client, clustersMap map[string]cluster.Cluster) {
	for {
		// (Re)populate the per-cluster health checkers if empty. Kept inside the loop so a
		// transient empty clustersMap at startup does not permanently disable health checking.
		if len(m.Cache) == 0 {
			m.populateCache(clustersMap, log)
		}

		log.Info("Running member cluster healthcheck")
		mdbmList := &mdbmulti.MongoDBMultiClusterList{}

		err := centralClient.List(ctx, mdbmList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("Failed to fetch MongoDBMultiClusterList from Kubernetes: %s", err)
		}

		// check the cluster health status corresponding to each member cluster
		for k, v := range m.Cache {
			if v.IsClusterHealthy(log) {
				log.Infof("Cluster %s reported healthy", k)
				if multicluster.ShouldPerformFailover() {
					continue
				}

				// If failover is disabled we should remove the cluster from the annotation after a number of health checks have succeeded
				m.mu.Lock()
				m.HealthyStreak[k] = min(m.HealthyStreak[k]+1, m.RequiredHealthyStreak)
				streak := m.HealthyStreak[k]
				m.mu.Unlock()
				if streak == m.RequiredHealthyStreak {
					for _, mdbm := range mdbmList.Items {
						if isInFailedClusterAnnotation(mdbm.Annotations, k) {
							log.Infof("Enqueuing resource: %s, because cluster %s has come back up", mdbm.Name, k)
							err := removeClusterFromFailedAnnotation(ctx, mdbm, k, centralClient)
							if err != nil {
								log.Errorf("Failed to remove cluster %s from failed annotation on %s: %s", k, mdbm.Name, err)
							}
							watchChannel <- event.GenericEvent{Object: &mdbm}
						}
					}
				}
				continue
			}

			log.Warnf("Cluster %s reported unhealthy", k)
			m.mu.Lock()
			m.HealthyStreak[k] = 0
			m.mu.Unlock()
			// re-enqueue all the MDBMultis the operator is watching into the reconcile loop
			for _, mdbm := range mdbmList.Items {
				if !isInFailedClusterAnnotation(mdbm.Annotations, k) && multicluster.ShouldPerformFailover() {
					log.Infof("Enqueuing resource: %s, because cluster %s has failed healthcheck", mdbm.Name, k)
					err := AddFailoverAnnotation(ctx, mdbm, k, centralClient)
					if err != nil {
						log.Errorf("Failed to add failover annotation to the mdbmc resource: %s, error: %s", mdbm.Name, err)
					}
					watchChannel <- event.GenericEvent{Object: &mdbm}
				} else if !isInFailedClusterAnnotation(mdbm.Annotations, k) {
					log.Infof("Marking resource: %s, with failed cluster %s annotation", mdbm.Name, k)
					err := addFailedClustersAnnotation(ctx, mdbm, k, centralClient)
					if err != nil {
						log.Errorf("Failed to add failed cluster annotation to the mdbmc resource: %s, error: %s", mdbm.Name, err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// isInFailedClusterAnnotation checks if the cluster name is present in the failedCluster annotation
func isInFailedClusterAnnotation(annotations map[string]string, clusterName string) bool {
	failedClusters := readFailedClusterAnnotation(annotations)
	if failedClusters == nil {
		return false
	}

	for _, c := range failedClusters {
		if c.ClusterName == clusterName {
			return true
		}
	}
	return false
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
func clusterWithMinimumMembers(clusters mdb.ClusterSpecList) int {
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
func distributeFailedMembers(clusters mdb.ClusterSpecList, clustername string) mdb.ClusterSpecList {
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
func AddFailoverAnnotation(ctx context.Context, mrs mdbmulti.MongoDBMultiCluster, clustername string, client kubernetesClient.Client) error {
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	err := addFailedClustersAnnotation(ctx, mrs, clustername, client)
	if err != nil {
		return err
	}

	currentClusterSpecs := mrs.Spec.ClusterSpecList
	currentClusterSpecs = distributeFailedMembers(currentClusterSpecs, clustername)

	updatedClusterSpec, err := json.Marshal(currentClusterSpecs)
	if err != nil {
		return err
	}

	return annotations.SetAnnotations(ctx, &mrs, map[string]string{failedcluster.ClusterSpecOverrideAnnotation: string(updatedClusterSpec)}, client)
}

func removeClusterFromFailedAnnotation(ctx context.Context, mrs mdbmulti.MongoDBMultiCluster, clustername string, client kubernetesClient.Client) error {
	failedClusters := readFailedClusterAnnotation(mrs.Annotations)

	remaining := slices.DeleteFunc(failedClusters, func(c failedcluster.FailedCluster) bool { return c.ClusterName == clustername })

	if len(remaining) == 0 {
		return annotations.RemoveAnnotation(ctx, &mrs, failedcluster.FailedClusterAnnotation, client)
	}

	clusterDataBytes, err := json.Marshal(remaining)
	if err != nil {
		return err
	}
	return annotations.SetAnnotations(ctx, &mrs, map[string]string{failedcluster.FailedClusterAnnotation: string(clusterDataBytes)}, client)
}

func addFailedClustersAnnotation(ctx context.Context, mrs mdbmulti.MongoDBMultiCluster, clustername string, client kubernetesClient.Client) error {
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	// read the existing failed cliuster annotations
	var clusterData []failedcluster.FailedCluster
	failedclusters := readFailedClusterAnnotation(mrs.Annotations)
	if failedclusters != nil {
		clusterData = failedclusters
	}

	clusterData = append(clusterData, failedcluster.FailedCluster{
		ClusterName: clustername,
		Members:     getClusterMembers(mrs.Spec.ClusterSpecList, clustername),
	})

	clusterDataBytes, err := json.Marshal(clusterData)
	if err != nil {
		return err
	}
	return annotations.SetAnnotations(ctx, &mrs, map[string]string{failedcluster.FailedClusterAnnotation: string(clusterDataBytes)}, client)
}

func getClusterMembers(clusterSpecList mdb.ClusterSpecList, clusterName string) int {
	for _, e := range clusterSpecList {
		if e.ClusterName == clusterName {
			return e.Members
		}
	}
	return 0
}
