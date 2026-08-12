package vaultwatcher

import (
	"context"
	"strconv"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func WatchSecretChangeForMDB(ctx context.Context, log *zap.SugaredLogger, watchChannel chan event.GenericEvent, k8sClient kubernetesClient.Client, vaultClient *vault.VaultClient, resourceType mdbv1.ResourceType) {
	for {
		mdbList := &mdbv1.MongoDBList{}
		err := k8sClient.List(ctx, mdbList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("failed to fetch MongoDBList from Kubernetes: %s", err)
		}

		for n, mdb := range mdbList.Items {
			// check if we care about the resource type, if not return early
			if mdb.Spec.ResourceType != resourceType {
				continue
			}
			// the credentials secret is mandatory and stored in a different path
			latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, vaultClient.OperatorScretMetadataPath(), mdb.Namespace, mdb.Spec.Credentials, mdb.Annotations, log)
			if latestResourceVersion > currentResourceVersion {
				watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
				break
			}

			for _, secretName := range mdb.GetSecretsMountedIntoDBPod() {
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, vaultClient.DatabaseSecretMetadataPath(), mdb.Namespace, secretName, mdb.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
					break
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
}

func WatchSecretChangeForOM(ctx context.Context, log *zap.SugaredLogger, watchChannel chan event.GenericEvent, k8sClient kubernetesClient.Client, vaultClient *vault.VaultClient) {
	for {
		omList := &omv1.MongoDBOpsManagerList{}
		err := k8sClient.List(ctx, omList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("failed to fetch MongoDBOpsManagerList from Kubernetes: %s", err)
		}

		triggeredReconciliation := false
		for n, om := range omList.Items {
			for _, secretName := range om.GetSecretsMountedIntoPod() {
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, vaultClient.OpsManagerSecretMetadataPath(), om.Namespace, secretName, om.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &omList.Items[n]}
					triggeredReconciliation = true
					break
				}
			}
			if triggeredReconciliation {
				break
			}
			for _, secretName := range om.Spec.AppDB.GetSecretsMountedIntoPod() {
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, vaultClient.AppDBSecretMetadataPath(), om.Namespace, secretName, om.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &omList.Items[n]}
					break
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
}

// getCurrentAndLatestVersion builds the Vault metadata path for the named
// secret under basePath and compares its version against the one recorded in
// the resource's annotations. The name originates from a CR field, so the path
// is built through vault.SecretPath rather than concatenated.
func getCurrentAndLatestVersion(vaultClient *vault.VaultClient, basePath, namespace, name string, annotations map[string]string, log *zap.SugaredLogger) (int, int) {
	annotationKey := name
	path, err := vault.SecretPath(basePath, namespace, name)
	if err != nil {
		log.Errorf("failed to build secret path for secret %s in namespace %s, err: %v", name, namespace, err)
		return -1, -1
	}

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
		currentResourceVersion, _ = strconv.Atoi(currentResourceAnnotation)
	}

	return latestResourceVersion, currentResourceVersion
}
