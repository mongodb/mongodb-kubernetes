package vault

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func WatchSecretChange(log *zap.SugaredLogger, watchChannel chan event.GenericEvent,
	k8sClient kubernetesClient.Client, vaultClient *VaultClient, resourceType mdbv1.ResourceType) {

	for {
		mdbList := &mdbv1.MongoDBList{}
		err := k8sClient.List(context.TODO(), mdbList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("failed to fetch MongoDBList from Kubernetes: %w", err)
		}

		for n, mdb := range mdbList.Items {
			// check if we care about the resource type, if not return early
			if mdb.Spec.ResourceType != resourceType {
				continue
			}
			// the credentials secret is mandatory and stored in a different path
			path := fmt.Sprintf("%s/%s/%s", OperatorSecretMetadataPath, mdb.Namespace, mdb.Spec.Credentials)
			latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, mdb.Spec.Credentials, mdb.Annotations, log)
			if latestResourceVersion > currentResourceVersion {
				watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
				break
			}

			for _, secretName := range mdb.GetSecretsMountedIntoDBPod() {
				path := fmt.Sprintf("%s/%s/%s", DatabaseSecretMetadataPath, mdb.Namespace, secretName)
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, secretName, mdb.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
					break
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
}

func getCurrentAndLatestVersion(vaultClient *VaultClient, path string, annotationKey string, annotations map[string]string, log *zap.SugaredLogger) (int, int) {
	latestResourceVersion, err := vaultClient.ReadSecretVersion(path)
	if err != nil {
		log.Errorf("failed to fetch secret revision for the path %s, err: %v", path, err)
	}

	// read the secret version from the annotation
	currentResourceAnnotation := annotations[annotationKey]

	var currentResourceVersion int
	if currentResourceAnnotation == "" {
		currentResourceVersion = latestResourceVersion
	} else {
		currentResourceVersion, err = strconv.Atoi(currentResourceAnnotation)
	}

	return latestResourceVersion, currentResourceVersion
}
