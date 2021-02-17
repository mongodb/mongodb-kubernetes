package watch

import (
	"reflect"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	userv1 "github.com/10gen/ops-manager-kubernetes/api/v1/user"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func PredicatesForUser() predicate.Funcs {
	return predicate.Funcs{
		// don't update users on status changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*userv1.MongoDBUser)
			newResource := e.ObjectNew.(*userv1.MongoDBUser)
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

			// ignore events that aren't related to our target Resource and any changes done to the status
			// (it's the controller that has made those changes, not user)
			return newResource.Spec.ResourceType == resourceType &&
				reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		}}
}
