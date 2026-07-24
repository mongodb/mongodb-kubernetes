package predicates

import (
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

// OnlyOnSpecChange returns a set of predicates indicating
// that reconciliations should only happen on changes to the Spec of the resource.
// Any other changes won't trigger a reconciliation, except the transition into
// deletion when a deletionTimestamp is set. This allows the controller to freely
// update annotations without triggering unintentional reconciliations while still
// ensuring finalizer-based cleanup can run.
func OnlyOnSpecChange() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbv1.MongoDBCommunity)
			newResource := e.ObjectNew.(*mdbv1.MongoDBCommunity)

			oldDeleting := oldResource.GetDeletionTimestamp() != nil
			newDeleting := newResource.GetDeletionTimestamp() != nil
			if !oldDeleting && newDeleting {
				return true
			}

			specChanged := !reflect.DeepEqual(oldResource.Spec, newResource.Spec)
			return specChanged
		},
	}
}
