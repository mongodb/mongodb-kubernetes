package watch

import (
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
)

func NewResourceWatcher() ResourceWatcher {
	return ResourceWatcher{
		WatchedResources: map[Object][]types.NamespacedName{},
	}
}

type ResourceWatcher struct {
	WatchedResources map[Object][]types.NamespacedName
}

// registerWatchedResources adds the secret/configMap -> mongodb resource pair to internal reconciler map. This allows
// to start watching for the events for this secret/configMap and trigger reconciliation for all depending mongodb resources
func (c *ResourceWatcher) RegisterWatchedResources(mongodbResourceNsName types.NamespacedName, configMap string, secret string) {
	defaultNamespace := mongodbResourceNsName.Namespace

	c.AddWatchedResourceIfNotAdded(configMap, defaultNamespace, ConfigMap, mongodbResourceNsName)
	c.AddWatchedResourceIfNotAdded(secret, defaultNamespace, Secret, mongodbResourceNsName)
}

// AddWatchedResourceIfNotAdded adds the given resource to the list of watched
// resources. A watched resource is a resource that, when changed, will trigger
// a reconciliation for its dependent resource.
func (c *ResourceWatcher) AddWatchedResourceIfNotAdded(name, namespace string,
	wType Type, dependentResourceNsName types.NamespacedName) {
	key := Object{
		ResourceType: wType,
		Resource: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
	if _, ok := c.WatchedResources[key]; !ok {
		c.WatchedResources[key] = make([]types.NamespacedName, 0)
	}
	found := false
	for _, v := range c.WatchedResources[key] {
		if v == dependentResourceNsName {
			found = true
		}
	}
	if !found {
		c.WatchedResources[key] = append(c.WatchedResources[key], dependentResourceNsName)
		zap.S().Debugf("Watching %s to trigger reconciliation for %s", key, dependentResourceNsName)
	}
}

// stop watching resources with input namespace and watched type, if any
func (c *ResourceWatcher) RemoveWatchedResources(namespace string, wType Type, dependentResourceNsName types.NamespacedName) {
	for key := range c.WatchedResources {
		if key.ResourceType == wType && key.Resource.Namespace == namespace {
			index := -1
			for i, v := range c.WatchedResources[key] {
				if v == dependentResourceNsName {
					index = i
				}
			}

			if index == -1 {
				continue
			}

			zap.S().Infof("Removing %s from resources dependent on %s", dependentResourceNsName, key)

			if index == 0 {
				if len(c.WatchedResources[key]) == 1 {
					delete(c.WatchedResources, key)
					continue
				}
				c.WatchedResources[key] = c.WatchedResources[key][index+1:]
				continue
			}

			if index == len(c.WatchedResources[key]) {
				c.WatchedResources[key] = c.WatchedResources[key][:index]
				continue
			}

			c.WatchedResources[key] = append(c.WatchedResources[key][:index], c.WatchedResources[key][index+1:]...)
		}
	}
}
