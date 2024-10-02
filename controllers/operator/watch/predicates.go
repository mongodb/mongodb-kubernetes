package watch

import (
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	appsv1 "k8s.io/api/apps/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

func PredicatesForUser() predicate.Funcs {
	return predicate.Funcs{
		// don't update users on status changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*userv1.MongoDBUser)
			newResource := e.ObjectNew.(*userv1.MongoDBUser)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}

			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	}
}

func PredicatesForOpsManager() predicate.Funcs {
	return predicate.Funcs{
		// don't update ops manager on status changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*omv1.MongoDBOpsManager)
			newResource := e.ObjectNew.(*omv1.MongoDBOpsManager)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}
			// check if any one of the vault annotations are different in revision
			if vault.IsVaultSecretBackend() {

				for _, e := range oldResource.GetSecretsMountedIntoPod() {
					if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
						return true
					}
				}

				for _, e := range oldResource.Spec.AppDB.GetSecretsMountedIntoPod() {
					if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
						return true
					}
				}
				return false
			}

			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	}
}

func PredicatesForMongoDB(resourceType mdbv1.ResourceType) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			resource := createEvent.Object.(*mdbv1.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			resource := deleteEvent.Object.(*mdbv1.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		GenericFunc: func(genericEvent event.GenericEvent) bool {
			resource := genericEvent.Object.(*mdbv1.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbv1.MongoDB)
			newResource := e.ObjectNew.(*mdbv1.MongoDB)

			oldSpecAnnotation := oldResource.GetAnnotations()[util.LastAchievedSpec]
			newSpecAnnotation := newResource.GetAnnotations()[util.LastAchievedSpec]

			// don't handle an update to just the previous spec annotation if they are not the same.
			// this prevents the operator triggering reconciliations on resource that it is updating itself.
			if !reflect.DeepEqual(oldSpecAnnotation, newSpecAnnotation) {
				return false
			}

			// check if any one of the vault annotations are different in revision
			if vault.IsVaultSecretBackend() {
				vaultReconcile := false
				credentialsAnnotation := newResource.Spec.Credentials
				if oldResource.GetAnnotations()[credentialsAnnotation] != newResource.GetAnnotations()[credentialsAnnotation] {
					vaultReconcile = true
				}
				for _, e := range oldResource.GetSecretsMountedIntoDBPod() {
					if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
						vaultReconcile = true
					}
				}
				return newResource.Spec.ResourceType == resourceType && vaultReconcile
			}

			// ignore events that aren't related to our target Resource and any changes done to the status
			// (it's the controller that has made those changes, not user)
			return newResource.Spec.ResourceType == resourceType &&
				reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	}
}

func PredicatesForStatefulSet() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSts := e.ObjectOld.(*appsv1.StatefulSet)
			newSts := e.ObjectNew.(*appsv1.StatefulSet)

			val, ok := newSts.Annotations["type"]

			if ok && val == "Replicaset" {
				if !reflect.DeepEqual(oldSts.Status, newSts.Status) && (newSts.Status.ReadyReplicas < *newSts.Spec.Replicas) {
					return true
				}
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
	}
}

// PredicatesForMultiStatefulSet is the predicate functions for the custom Statefulset Event
// handler used for Multicluster reconciler
func PredicatesForMultiStatefulSet() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSts := e.ObjectOld.(*appsv1.StatefulSet)
			newSts := e.ObjectNew.(*appsv1.StatefulSet)

			// check if it is owned by MultiCluster CR first
			if _, ok := newSts.Annotations[handler.MongoDBMultiResourceAnnotation]; !ok {
				return false
			}

			return !reflect.DeepEqual(oldSts.Status, newSts.Status)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			_, ok := e.Object.GetAnnotations()[handler.MongoDBMultiResourceAnnotation]
			return ok
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
	}
}
