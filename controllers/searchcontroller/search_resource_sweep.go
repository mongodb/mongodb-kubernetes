package searchcontroller

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
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
