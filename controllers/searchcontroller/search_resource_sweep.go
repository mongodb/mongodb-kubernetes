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

// SearchResourceCleanup describes one kind of label-owned Search resource for
// SweepSearchResources: how candidates are enumerated and which ones to delete.
type SearchResourceCleanup struct {
	// Kind is the human-readable kind for logs and errors, e.g. "state ConfigMap".
	Kind string
	// Name restricts the sweep to the object with this exact name (operator
	// singletons such as the state ConfigMap and auth Secrets).
	Name string
	// Component restricts candidates to the given component label value.
	Component string
	// OmitCluster matches this Search's objects on any cluster: the sweep's
	// cluster scope is ignored. Identity is still enforced by the owner labels.
	OmitCluster bool
	// Eligible is an extra per-object filter; nil sweeps every owned candidate.
	Eligible func(client.Object) bool
	// DeleteOpts apply to every delete of this kind (e.g. foreground
	// propagation for Deployments).
	DeleteOpts []client.DeleteOption
	NewList    func() client.ObjectList
}

// SweepSearchResources deletes one cluster's label-owned resources of the
// given MongoDBSearch: cached List per descriptor, ownership gate per object,
// then delete. Failures are collected per object and never abort the sweep.
// Returns whether any owned candidate was found, deleted or not — callers
// waiting for a resource to be fully gone gate on it.
func SweepSearchResources(ctx context.Context, c kubernetesClient.Client, search *searchv1.MongoDBSearch, clusterName string, cleanups []SearchResourceCleanup, log *zap.SugaredLogger) (bool, error) {
	found := false
	var errs error
	for _, cleanup := range cleanups {
		selector := client.MatchingLabels(khandler.SearchManagedLabels(search, "", cleanup.Component, ""))
		if clusterName != "" && !cleanup.OmitCluster {
			selector[khandler.MongoDBSearchClusterNameLabel] = clusterName
		}
		list := cleanup.NewList()
		if err := c.List(ctx, list, client.InNamespace(search.Namespace), selector); err != nil {
			errs = errors.Join(errs, xerrors.Errorf("failed listing MongoDBSearch %ss on cluster %q: %w", cleanup.Kind, clusterName, err))
			continue
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			errs = errors.Join(errs, xerrors.Errorf("failed extracting MongoDBSearch %s list on cluster %q: %w", cleanup.Kind, clusterName, err))
			continue
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok || (cleanup.Name != "" && obj.GetName() != cleanup.Name) {
				continue
			}
			// The selector already scopes candidates to this Search's identity
			// labels; the gate additionally requires the cluster-name label to
			// match the sweep's cluster (absent for the empty cluster name).
			if !cleanup.OmitCluster && !khandler.HasSearchOwnership(obj, search, clusterName) {
				continue
			}
			if cleanup.Eligible != nil && !cleanup.Eligible(obj) {
				continue
			}
			found = true
			if err := c.Delete(ctx, obj, cleanup.DeleteOpts...); err != nil && !apierrors.IsNotFound(err) {
				errs = errors.Join(errs, xerrors.Errorf("failed deleting MongoDBSearch %s %s on cluster %q: %w", cleanup.Kind, obj.GetName(), clusterName, err))
				continue
			}
			log.Infof("Deleted MongoDBSearch %s %s (cluster=%q)", cleanup.Kind, obj.GetName(), clusterName)
		}
	}
	return found, errs
}
