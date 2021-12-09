package vaultwatcher

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func WatchSecretChangeForMDB(log *zap.SugaredLogger, watchChannel chan event.GenericEvent,
	k8sClient kubernetesClient.Client, vaultClient *vault.VaultClient, resourceType mdbv1.ResourceType) {

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
			path := fmt.Sprintf("%s/%s/%s", vaultClient.OperatorScretMetadataPath(), mdb.Namespace, mdb.Spec.Credentials)
			latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, mdb.Spec.Credentials, mdb.Annotations, log)
			if latestResourceVersion > currentResourceVersion {
				watchChannel <- event.GenericEvent{Object: &mdbList.Items[n]}
				break
			}

			for _, secretName := range mdb.GetSecretsMountedIntoDBPod() {
				path := fmt.Sprintf("%s/%s/%s", vaultClient.DatabaseSecretMetadataPath(), mdb.Namespace, secretName)
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

func WatchSecretChangeForOM(log *zap.SugaredLogger, watchChannel chan event.GenericEvent, k8sClient kubernetesClient.Client, vaultClient *vault.VaultClient) {

	for {
		omList := &omv1.MongoDBOpsManagerList{}
		err := k8sClient.List(context.TODO(), omList, &client.ListOptions{Namespace: ""})
		if err != nil {
			log.Errorf("failed to fetch MongoDBOpsManagerList from Kubernetes: %w", err)
		}

		triggeredReconciliation := false
		for n, om := range omList.Items {
			for _, secretName := range om.GetSecretsMountedIntoPod() {
				path := fmt.Sprintf("%s/%s/%s", vaultClient.OpsManagerSecretMetadataPath(), om.Namespace, secretName)
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, secretName, om.Annotations, log)

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
				path := fmt.Sprintf("%s/%s/%s", vaultClient.AppDBSecretMetadataPath(), om.Namespace, secretName)
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, secretName, om.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &omList.Items[n]}
					break
				}
			}
		}

		time.Sleep(10 * time.Second)
	}

}

func getCurrentAndLatestVersion(vaultClient *vault.VaultClient, path string, annotationKey string, annotations map[string]string, log *zap.SugaredLogger) (int, int) {
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
