package certs

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/configmap"
)

// MemberClusterTarget is one member cluster that the operator copies the managed CA bundles to.
type MemberClusterTarget struct {
	Name   string
	Client kubernetesClient.Client
}

// ReplicateManagedCABundlesToMemberClusters copies the operator-owned CA bundle ConfigMaps from the
// cluster the operator runs in to the given member clusters.
//
// The pods of a multi-cluster deployment run in the member clusters and mount these bundles, but
// the operator creates them only in its own cluster, so without the copy the pods have no CA to
// trust and TLS fails.
//
// The copies carry ownerLabels, so that they are removed with the resource: the cleanup on delete
// finds the resources it removes by label, and the bundles are not owned across clusters. Whatever
// owner references a copy already has are kept, so a cluster that is both the operator's cluster
// and a member does not lose the owner reference the operator set on the ConfigMap.
func ReplicateManagedCABundlesToMemberClusters(
	ctx context.Context,
	central kubernetesClient.Client,
	namespace string,
	bundleNames []string,
	targets []MemberClusterTarget,
	ownerLabels map[string]string,
	log *zap.SugaredLogger,
) workflow.Status {
	for _, name := range bundleNames {
		cm, err := central.GetConfigMap(ctx, kube.ObjectKey(namespace, name))
		if err != nil {
			return workflow.Failed(xerrors.Errorf("expected managed CA ConfigMap %s not found on the operator cluster: %w", name, err))
		}
		for _, target := range targets {
			memberCM := configmap.Builder().
				SetName(name).
				SetNamespace(namespace).
				SetData(cm.Data).
				SetLabels(ownerLabels).
				Build()
			if existing, getErr := target.Client.GetConfigMap(ctx, kube.ObjectKey(namespace, name)); getErr == nil {
				memberCM.OwnerReferences = existing.OwnerReferences
			}
			if err := configmap.CreateOrUpdate(ctx, target.Client, memberCM); err != nil && !apiErrors.IsAlreadyExists(err) {
				return workflow.Failed(xerrors.Errorf("failed to copy managed CA ConfigMap %s in cluster %s: %w", name, target.Name, err))
			}
			log.Debugf("Ensured managed CA ConfigMap %s/%s in cluster %s", namespace, name, target.Name)
		}
	}
	return workflow.OK()
}
