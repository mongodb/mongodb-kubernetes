package memberwatch

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/annotations"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type MemberClusterMap struct {
	Cache map[string]*MemberHeathCheck
}

// WatchMemberClusterHealth watches member clusters healthcheck. If a cluster fails healthcheck it re-enques the
// MongoDBMulti resources. It is spun up in the mongodb multi reconciler as a go-routine, and is executed every 10 seconds.
func (m MemberClusterMap) WatchMemberClusterHealth(log *zap.SugaredLogger, watchChannel chan event.GenericEvent,
	memberClients map[string]kubernetesClient.Client,
	centralClient kubernetesClient.Client) {

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

			server := kubeConfig.Clusters[n].Cluster.Server
			certificateAuthority, err := base64.StdEncoding.DecodeString(kubeConfig.Clusters[n].Cluster.CertificateAuthority)
			if err != nil {
				log.Errorf("Failed to decode certificate for cluster: %s, err: %w", clusterName, err)
				continue
			}

			token := kubeConfig.Users[n].User.Token

			m.Cache[clusterName] = NewMemberHealthCheck(server, certificateAuthority, token)

		}
	}

	for {
		log.Info("Running member cluster healthcheck")
		mdbmList := &mdbmulti.MongoDBMultiList{}

		err := centralClient.List(context.TODO(), mdbmList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("Failed to fetch MongoDBMultiList from Kubernetes : %w", err)
		}

		// check the cluster health status corresponding to each member cluster
		for k, v := range m.Cache {
			if v.IsClusterHealthy(log) {
				log.Infof("Cluster %s reported healthy", k)
				continue
			}
			// re-enqueue all the MDBMultis the operator is watching into the reconcile loop
			for _, mdbm := range mdbmList.Items {
				log.Infof("Enqueuing resource: %s, because cluster %s has failed healthcheck", mdbm.Name, k)

				err := addFailoverAnnotation(mdbm, k, centralClient)
				if err != nil {
					log.Errorf("Failed to add failover annotation to the mdbm resource: %s, error: %s", mdbm.Name, err)
				}
				watchChannel <- event.GenericEvent{Object: &mdbm}
			}

		}
		time.Sleep(10 * time.Second)
	}

}

func addFailoverAnnotation(mrs mdbmulti.MongoDBMulti, clustername string, client kubernetesClient.Client) error {
	if mrs.Annotations == nil {
		mrs.Annotations = map[string]string{}
	}

	// TODO: add a dummy annotation for now and fix it later with something more sane
	return annotations.SetAnnotations(mrs.DeepCopy(), map[string]string{"failedCluster": clustername}, client)
}
