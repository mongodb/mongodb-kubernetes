package vault

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func WatchSecretChange(log *zap.SugaredLogger, watchChannel chan event.GenericEvent, path string,
	k8sClient kubernetesClient.Client, vaultClient *VaultClient, resourceType mdbv1.ResourceType) {

	for {
		mdbList := &mdbv1.MongoDBList{}
		err := k8sClient.List(context.TODO(), mdbList)
		if err != nil {
			log.Errorf("failed to fetch MongoDBList from Kubernetes: %w", err)
		}

		for n, mdb := range mdbList.Items {
			// check if we care about the resource type, if not return early
			if mdb.Spec.ResourceType != resourceType {
				continue
			}

			// fetch the secret version corresponding to this CR and check the path
			latestResourceVersion, err := vaultClient.ReadSecretVersion(path)
			if err != nil {
				log.Errorf("failed to fectch secret revision for the path %s, err: %v", path, err)
			}

			// read the secret version from the annotation
			currentResourceAnnotation := mdb.Annotations["agent-certs"]
			currentResourceVersion, _ := strconv.Atoi(currentResourceAnnotation)

			if latestResourceVersion > currentResourceVersion {
				watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
			}
		}

		time.Sleep(10 * time.Second)
	}
}

func GetSecretPaths(namespace string) []string {
	return []string{
		fmt.Sprintf("%s/%s/agent-certs", DatabaseSecretMetadataPath, namespace),
	}
}
