package watch

import (
	"context"
	"fmt"
	"reflect"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
)

// Type is an enum for all kubernetes types watched by controller for changes for configuration
type Type string

const (
	ConfigMap          Type = "ConfigMap"
	Secret             Type = "Secret"
	MongoDB            Type = "MongoDB"
	ClusterMongoDBRole Type = "ClusterMongoDBRole"
)

// the Object watched by controller. Includes its type and namespace+name
type Object struct {
	ResourceType Type
	Resource     types.NamespacedName
}

func (w Object) String() string {
	return fmt.Sprintf("%s (%s)", w.Resource, w.ResourceType)
}

// ResourcesHandler is a special implementation of 'handler.EventHandler' that checks if the event for
// K8s Resource must trigger reconciliation for any Operator managed Resource (MongoDB, MongoDBOpsManager). This is
// done via consulting the 'TrackedResources' map. The map is stored in the relevant reconciler which puts pairs
// [K8s_resource_name -> operator_managed_resource_name] there as
// soon as reconciliation happens for the Resource
type ResourcesHandler struct {
	ResourceType    Type
	ResourceWatcher *ResourceWatcher
}

// Note that we implement Create in addition to Update to be able to handle cases when config map or secret is deleted
// and then created again.
func (c *ResourcesHandler) Create(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	c.doHandle(e.Object.GetNamespace(), e.Object.GetName(), q)
}

func (c *ResourcesHandler) Update(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if !shouldHandleUpdate(e) {
		return
	}
	c.doHandle(e.ObjectOld.GetNamespace(), e.ObjectOld.GetName(), q)
}

// shouldHandleUpdate return true if the update event must be handled. This shouldn't happen if data for watched
// ConfigMap or Secret hasn't changed
func shouldHandleUpdate(e event.UpdateEvent) bool {
	switch v := e.ObjectOld.(type) {
	case *corev1.ConfigMap:
		return !reflect.DeepEqual(v.Data, e.ObjectNew.(*corev1.ConfigMap).Data)
	case *corev1.Secret:
		return !reflect.DeepEqual(v.Data, e.ObjectNew.(*corev1.Secret).Data)
	}
	return true
}

func (c *ResourcesHandler) doHandle(namespace, name string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	object := Object{
		ResourceType: c.ResourceType,
		Resource:     types.NamespacedName{Name: name, Namespace: namespace},
	}

	for _, v := range c.ResourceWatcher.GetWatchedResources()[object] {
		zap.S().Infof("%s has been modified -> triggering reconciliation for dependent Resource %s", object, v)
		q.Add(reconcile.Request{NamespacedName: v})
	}
}

// Seems we don't need to react on config map/secret removal..
func (c *ResourcesHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	switch e.Object.(type) {
	case *corev1.ConfigMap:
		return
	case *corev1.Secret:
		return
	}

	c.doHandle(e.Object.GetNamespace(), e.Object.GetName(), q)
}

func (c *ResourcesHandler) Generic(context.Context, event.TypedGenericEvent[client.Object], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}
