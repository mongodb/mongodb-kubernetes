package memberwatch

import (
	"context"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func WatchMemeberClusterHealth(log *zap.SugaredLogger, watchChannel chan event.GenericEvent,
	memberClients map[string]kubernetesClient.Client,
	centralClient kubernetesClient.Client) {

	for {
		mdbmList := &mdbmulti.MongoDBMultiList{}

		err := centralClient.List(context.TODO(), mdbmList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("Failed to fetch MongoDBMultiList from Kubernetes : %w", err)
		}
		// TODO: add logic to get cluster health status here and re-enqueue objects (CLOUDP-134304)
	}

}
