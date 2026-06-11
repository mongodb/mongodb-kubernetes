package searchcontroller

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// staleSweepExpectations are the resource names the current reconcile plan wants
// to exist. Anything search-owned carrying a matching component label that is
// NOT in these sets is stale and gets deleted.
type staleSweepExpectations struct {
	services     map[string]bool // proxy + headless + cluster-level
	statefulSets map[string]bool
	configMaps   map[string]bool
}

// OnDelete deletes every search-owned mongot StatefulSet, headless Service,
// ConfigMap, and proxy Service across the central and all member clusters by
// running the stale sweep with empty expectations. PVCs are not deleted
// explicitly: the mongot StatefulSet sets persistentVolumeClaimRetentionPolicy
// whenDeleted=Delete (see search_construction.go), so deleting the StatefulSet
// reaps its PVCs — same posture as sharded MC, which doesn't delete PVCs either.
func (r *MongoDBSearchReconcileHelper) OnDelete(ctx context.Context, log *zap.SugaredLogger) error {
	return r.cleanupStaleResources(ctx, log, staleSweepExpectations{})
}

// cleanupStaleResources deletes search-owned mongot StatefulSets, Services, and
// ConfigMaps (plus proxy Services in managed-LB mode) that the current reconcile
// plan no longer expects. It runs across the central client and every member
// cluster, because in MC these resources live on the member clusters and aren't
// GC'd by Kubernetes (owner refs don't cross cluster boundaries).
//
// A List/Delete failure on one cluster does not abort the others: per-cluster
// errors are collected and joined so an unreachable member can't strand stale
// resources on a healthy one.
func (r *MongoDBSearchReconcileHelper) cleanupStaleResources(ctx context.Context, log *zap.SugaredLogger, expected staleSweepExpectations) error {
	// In unmanaged-LB sharded mode the operator doesn't own the proxy Services,
	// so only mongot kinds are swept; the user owns the proxy Services.
	sweepProxy := !r.mdbSearch.IsShardedUnmanagedLB()

	clients := map[string]kubernetesClient.Client{"": r.client}
	for name, c := range r.memberClusterClients {
		clients[name] = c
	}

	var errs []error
	for clusterName, c := range clients {
		errs = append(errs, r.sweepStatefulSets(ctx, log, clusterName, c, expected.statefulSets))
		errs = append(errs, r.sweepConfigMaps(ctx, log, clusterName, c, expected.configMaps))
		errs = append(errs, r.sweepServices(ctx, log, clusterName, c, expected.services, sweepProxy))
	}
	return errors.Join(errs...)
}

func (r *MongoDBSearchReconcileHelper) sweepStatefulSets(ctx context.Context, log *zap.SugaredLogger, clusterName string, c kubernetesClient.Client, expected map[string]bool) error {
	list := &appsv1.StatefulSetList{}
	if err := c.List(ctx, list, client.InNamespace(r.mdbSearch.Namespace), client.MatchingLabels{componentLabelKey: mongotComponent}); err != nil {
		return xerrors.Errorf("failed to list mongot StatefulSets on cluster %q: %w", clusterName, err)
	}
	var errs []error
	for i := range list.Items {
		errs = append(errs, r.deleteIfStale(ctx, log, clusterName, c, &list.Items[i], "StatefulSet", expected))
	}
	return errors.Join(errs...)
}

func (r *MongoDBSearchReconcileHelper) sweepConfigMaps(ctx context.Context, log *zap.SugaredLogger, clusterName string, c kubernetesClient.Client, expected map[string]bool) error {
	list := &corev1.ConfigMapList{}
	if err := c.List(ctx, list, client.InNamespace(r.mdbSearch.Namespace), client.MatchingLabels{componentLabelKey: mongotComponent}); err != nil {
		return xerrors.Errorf("failed to list mongot ConfigMaps on cluster %q: %w", clusterName, err)
	}
	var errs []error
	for i := range list.Items {
		errs = append(errs, r.deleteIfStale(ctx, log, clusterName, c, &list.Items[i], "ConfigMap", expected))
	}
	return errors.Join(errs...)
}

// sweepServices sweeps both mongot headless Services and (when sweepProxy)
// proxy Services. Both component labels are listed in one pass against the same
// expected-names set.
func (r *MongoDBSearchReconcileHelper) sweepServices(ctx context.Context, log *zap.SugaredLogger, clusterName string, c kubernetesClient.Client, expected map[string]bool, sweepProxy bool) error {
	components := []string{mongotComponent}
	if sweepProxy {
		components = append(components, proxyServiceComponent)
	}

	var errs []error
	for _, component := range components {
		list := &corev1.ServiceList{}
		if err := c.List(ctx, list, client.InNamespace(r.mdbSearch.Namespace), client.MatchingLabels{componentLabelKey: component}); err != nil {
			errs = append(errs, xerrors.Errorf("failed to list %q Services on cluster %q: %w", component, clusterName, err))
			continue
		}
		for i := range list.Items {
			errs = append(errs, r.deleteIfStale(ctx, log, clusterName, c, &list.Items[i], "Service", expected))
		}
	}
	return errors.Join(errs...)
}

// deleteIfStale deletes obj when it is owned by this search but not in the
// expected-names set. Ownership on the central cluster is by owner-ref UID; on
// member clusters it's by the search-owner labels (owner refs don't cross
// cluster boundaries). NotFound on delete is tolerated.
func (r *MongoDBSearchReconcileHelper) deleteIfStale(ctx context.Context, log *zap.SugaredLogger, clusterName string, c kubernetesClient.Client, obj client.Object, kind string, expected map[string]bool) error {
	if expected[obj.GetName()] || !r.ownsForSweep(clusterName, obj) {
		return nil
	}
	log.Infof("Deleting stale %s %s on cluster %q", kind, obj.GetName(), clusterName)
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return xerrors.Errorf("failed to delete stale %s %s on cluster %q: %w", kind, obj.GetName(), clusterName, err)
	}
	return nil
}

// ownsForSweep reports whether the search owns obj for cleanup purposes: by
// owner-ref UID on the central cluster, by owner labels on member clusters.
func (r *MongoDBSearchReconcileHelper) ownsForSweep(clusterName string, obj client.Object) bool {
	if clusterName == "" {
		return isOwnedBy(obj, r.mdbSearch)
	}
	labels := obj.GetLabels()
	return labels[khandler.MongoDBSearchOwnerNameLabel] == r.mdbSearch.Name &&
		labels[khandler.MongoDBSearchOwnerNamespaceLabel] == r.mdbSearch.Namespace
}
