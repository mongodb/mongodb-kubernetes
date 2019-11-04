package operator

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func predicatesForUser() predicate.Funcs {
	return predicate.Funcs{
		// don't update users on status changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbv1.MongoDBUser)
			newResource := e.ObjectNew.(*mdbv1.MongoDBUser)
			return shouldReconcile(oldResource, newResource)
		},
	}
}

func predicatesForOpsManager() predicate.Funcs {
	return predicate.Funcs{
		// don't update ops manager on status changes
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbv1.MongoDBOpsManager)
			newResource := e.ObjectNew.(*mdbv1.MongoDBOpsManager)
			return shouldReconcile(oldResource, newResource)
		},
	}
}

func predicatesFor(resourceType mdbv1.ResourceType) predicate.Funcs {
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

			// we don't support type change so if the status has been assigned a resource type
			// we want the corresponding controller to handle the resource.
			if newResource.Status.ResourceType == resourceType {
				return shouldReconcile(oldResource, newResource)
			}

			// ignore events that aren't related to our target resource
			if oldResource.Spec.ResourceType != resourceType {
				return false
			}
			// if the status/spec resource type is different from the old spec, we are changing back
			// from an invalid state. Remove after implementing type change functionality
			if newResource.Status.ResourceType != resourceType && newResource.Spec.ResourceType != resourceType {
				return false
			}
			return shouldReconcile(oldResource, newResource)
		}}
}
