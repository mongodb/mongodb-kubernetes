package operator

import (
	"fmt"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// watchedType is an enum for all kubernetes types watched by controller for changes for configuration
type watchedType string

const (
	ConfigMap watchedType = "ConfigMap"
	Secret    watchedType = "Secret"
)

// the  object watched by controller. Includes its type and namespace+name
type watchedObject struct {
	resourceType watchedType
	resource     types.NamespacedName
}

func (w watchedObject) String() string {
	return fmt.Sprintf("%s (%s)", w.resource, w.resourceType)
}

// ConfigMapAndSecretHandler is a special implementation of 'handler.EventHandler' that checks if the event for
// ConfigMap/Secret must trigger reconciliation for any Mongodb resource. This is done via consulting the 'trackedResources'
// map. The map is stored in relevant reconciler which puts pairs [configmap/secret -> mongodb_resource_name] there as
// soon as reconciliation happens for the resource
type ConfigMapAndSecretHandler struct {
	resourceType     watchedType
	trackedResources map[watchedObject][]types.NamespacedName
}

// Note that we implement Create in addition to Update to be able to handle cases when config map or secret is deleted
// and then created again.
func (c *ConfigMapAndSecretHandler) Create(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	c.doHandle(e.Meta.GetNamespace(), e.Meta.GetName(), q)
}

func (c *ConfigMapAndSecretHandler) Update(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	c.doHandle(e.MetaOld.GetNamespace(), e.MetaOld.GetName(), q)
}

func (c *ConfigMapAndSecretHandler) doHandle(namespace, name string, q workqueue.RateLimitingInterface) {
	configMapOrSecret := watchedObject{
		resourceType: c.resourceType,
		resource:     types.NamespacedName{Name: name, Namespace: namespace},
	}
	for _, v := range c.trackedResources[configMapOrSecret] {
		zap.S().Infof("%s has been modified -> triggering reconciliation for dependent resource %s", configMapOrSecret, v)
		q.Add(reconcile.Request{NamespacedName: v})
	}
}

// Seems we don't need to react on config map/secret removal..
func (c *ConfigMapAndSecretHandler) Delete(event.DeleteEvent, workqueue.RateLimitingInterface) {}

func (c *ConfigMapAndSecretHandler) Generic(event.GenericEvent, workqueue.RateLimitingInterface) {}
