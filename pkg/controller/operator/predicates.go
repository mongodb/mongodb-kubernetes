package operator

import (
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func predicatesFor(resourceType mongodb.ResourceType) predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			resource := createEvent.Object.(*mongodb.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			resource := deleteEvent.Object.(*mongodb.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		GenericFunc: func(genericEvent event.GenericEvent) bool {
			resource := genericEvent.Object.(*mongodb.MongoDB)
			return resource.Spec.ResourceType == resourceType
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mongodb.MongoDB)
			newResource := e.ObjectNew.(*mongodb.MongoDB)

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

// predicatesForProject returns predicates that will filter out any config maps
// that don't have all the required fields which indicate that a config map is a Project ConfigMap
func predicatesForProject() predicate.Funcs {
	hasRequiredParams := func(configMap *v1.ConfigMap) bool {
		_, hasBaseUrl := configMap.Data[util.OmBaseUrl]
		_, hasOrgId := configMap.Data[util.OmOrgId]
		return hasBaseUrl && hasOrgId
	}
	return predicate.Funcs{
		CreateFunc: func(createEvent event.CreateEvent) bool {
			return hasRequiredParams(createEvent.Object.(*v1.ConfigMap))
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return hasRequiredParams(updateEvent.ObjectOld.(*v1.ConfigMap)) && hasRequiredParams(updateEvent.ObjectNew.(*v1.ConfigMap))
		}}
}
