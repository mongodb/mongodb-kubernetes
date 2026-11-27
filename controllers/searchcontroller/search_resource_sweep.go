package searchcontroller

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	khandler "github.com/mongodb/mongodb-kubernetes/pkg/handler"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// deleteOwned lists this Search's label-owned objects of one kind (component=""
// selects on owner labels only) and deletes every one accepted by eligible
// (nil = all). Per-object failures are joined and never abort the sweep;
// NotFound deletes are tolerated. clusterName is log context only.
func deleteOwned(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName, kind, component string, list client.ObjectList, eligible func(client.Object) bool, log *zap.SugaredLogger) error {
	selector := client.MatchingLabels(khandler.SearchOwnershipLabels(search, "", component))
	if err := c.List(ctx, list, client.InNamespace(search.Namespace), selector); err != nil {
		return xerrors.Errorf("failed listing MongoDBSearch %ss on cluster %q: %w", kind, clusterName, err)
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return xerrors.Errorf("failed extracting MongoDBSearch %s list on cluster %q: %w", kind, clusterName, err)
	}
	var errs error
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok || (eligible != nil && !eligible(obj)) {
			continue
		}
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			errs = errors.Join(errs, xerrors.Errorf("failed deleting MongoDBSearch %s %s on cluster %q: %w", kind, obj.GetName(), clusterName, err))
			continue
		}
		log.Infof("Deleted MongoDBSearch %s %s (cluster=%q)", kind, obj.GetName(), clusterName)
	}
	return errs
}

// DeleteAllOwnedResources deletes every owned object of one kind/component — the
// broad removed-cluster sweeps.
func DeleteAllOwnedResources(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName, kind, component string, list client.ObjectList, log *zap.SugaredLogger) error {
	return deleteOwned(ctx, c, search, clusterName, kind, component, list, nil, log)
}

// DeleteOwnedResource deletes one exact-name operator singleton after verifying
// it carries this Search's ownership labels (and component, when non-empty) —
// a same-name object owned by someone else is silently left alone, matching the
// label-selected sweeps. found reports whether the owned object existed BEFORE
// the delete attempt (even if the delete then fails): the metrics controller's
// forwarder-before-host Pending gate relies on exists-semantics, and Foreground
// propagation keeps it true until the pods are fully gone.
func DeleteOwnedResource(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName, kind, component string, obj client.Object, log *zap.SugaredLogger, opts ...client.DeleteOption) (found bool, err error) {
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, xerrors.Errorf("failed getting MongoDBSearch %s %s on cluster %q: %w", kind, obj.GetName(), clusterName, err)
	}
	if !khandler.HasSearchOwnership(obj, search) ||
		(component != "" && obj.GetLabels()[khandler.MongoDBSearchComponentLabel] != component) {
		return false, nil
	}
	if err := c.Delete(ctx, obj, opts...); err != nil && !apierrors.IsNotFound(err) {
		return true, xerrors.Errorf("failed deleting MongoDBSearch %s %s on cluster %q: %w", kind, obj.GetName(), clusterName, err)
	}
	log.Infof("Deleted MongoDBSearch %s %s (cluster=%q)", kind, obj.GetName(), clusterName)
	return true, nil
}

// DeleteResizeOrphanedPods deletes the pods of the StatefulSets in status.pvc that have no controller:
// between a resize's orphan-delete and the recreate, neither the StatefulSet sweep nor garbage
// collection removes them.
func DeleteResizeOrphanedPods(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, log *zap.SugaredLogger) error {
	if len(search.Status.PVCs) == 0 {
		return nil
	}
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(search.Namespace)); err != nil {
		return xerrors.Errorf("failed listing pods on cluster %q: %w", clusterName, err)
	}
	var errs error
	for i := range pods.Items {
		pod := &pods.Items[i]
		orphanedByResize := slices.ContainsFunc(search.Status.PVCs, func(p status.PVC) bool {
			ordinal, ok := strings.CutPrefix(pod.Name, p.StatefulsetName+"-")
			_, err := strconv.Atoi(ordinal)
			return ok && err == nil
		})
		if !orphanedByResize || metav1.GetControllerOf(pod) != nil {
			continue
		}
		if err := c.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			errs = errors.Join(errs, xerrors.Errorf("failed deleting pod %s on cluster %q: %w", pod.Name, clusterName, err))
			continue
		}
		log.Infof("Deleted pod %s orphaned by a PVC resize (cluster=%q)", pod.Name, clusterName)
	}
	return errs
}
