package watch

import (
	"sync"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
)

func NewResourceWatcher() *ResourceWatcher {
	return &ResourceWatcher{
		watchedResources: map[Object][]types.NamespacedName{},
	}
}

type ResourceWatcher struct {
	mapLock          sync.RWMutex
	watchedResources map[Object][]types.NamespacedName
}

// GetWatchedResources returns map of watched resources.
// It is returning deep copy, because underlying map can be modified concurrently.
func (r *ResourceWatcher) GetWatchedResources() map[Object][]types.NamespacedName {
	r.mapLock.RLock()
	defer r.mapLock.RUnlock()

	watchedResourcesCopy := map[Object][]types.NamespacedName{}
	for obj, namespaces := range r.watchedResources {
		namespacesCopy := make([]types.NamespacedName, len(namespaces))
		copy(namespacesCopy, namespaces)
		watchedResourcesCopy[obj] = namespacesCopy
	}

	return watchedResourcesCopy
}

// RegisterWatchedMongodbResources adds the secret/configMap -> mongodb resource pair to internal reconciler map. This allows
// to start watching for the events for this secret/configMap and trigger reconciliation for all depending mongodb resources
func (r *ResourceWatcher) RegisterWatchedMongodbResources(mongodbResourceNsName types.NamespacedName, configMap string, secret string) {
	defaultNamespace := mongodbResourceNsName.Namespace

	r.AddWatchedResourceIfNotAdded(configMap, defaultNamespace, ConfigMap, mongodbResourceNsName)
	r.AddWatchedResourceIfNotAdded(secret, defaultNamespace, Secret, mongodbResourceNsName)
}

// GetWatchedResourcesOfType returns all watched resources of the given type in the specified namespace.
// if the specified namespace is the zero value, resources in all namespaces will be returned.
func (r *ResourceWatcher) GetWatchedResourcesOfType(wType Type, ns string) []types.NamespacedName {
	r.mapLock.RLock()
	defer r.mapLock.RUnlock()

	var res []types.NamespacedName
	for k := range r.watchedResources {
		if k.ResourceType != wType {
			continue
		}
		if k.Resource.Namespace == ns || ns == "" {
			res = append(res, k.Resource)
		}
	}
	return res
}

// RegisterWatchedTLSResources adds the CA configMap and a slice of TLS secrets to the list of watched resources.
func (r *ResourceWatcher) RegisterWatchedTLSResources(mongodbResourceNsName types.NamespacedName, caConfigMap string, tlsSecrets []string) {
	defaultNamespace := mongodbResourceNsName.Namespace

	if caConfigMap != "" {
		r.AddWatchedResourceIfNotAdded(caConfigMap, defaultNamespace, ConfigMap, mongodbResourceNsName)
	}

	for _, tlsSecret := range tlsSecrets {
		r.AddWatchedResourceIfNotAdded(tlsSecret, defaultNamespace, Secret, mongodbResourceNsName)
	}
}

// RemoveDependentWatchedResources stops watching resources related to the input resource
func (r *ResourceWatcher) RemoveDependentWatchedResources(resourceNsName types.NamespacedName) {
	r.RemoveAllDependentWatchedResources(resourceNsName.Namespace, resourceNsName)
}

// AddWatchedResourceIfNotAdded adds the given resource to the list of watched
// resources. A watched resource is a resource that, when changed, will trigger
// a reconciliation for its dependent resource.
func (r *ResourceWatcher) AddWatchedResourceIfNotAdded(name, namespace string, wType Type, dependentResourceNsName types.NamespacedName) {
	r.mapLock.Lock()
	defer r.mapLock.Unlock()
	key := Object{
		ResourceType: wType,
		Resource: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
	if _, ok := r.watchedResources[key]; !ok {
		r.watchedResources[key] = make([]types.NamespacedName, 0)
	}
	found := false
	for _, v := range r.watchedResources[key] {
		if v == dependentResourceNsName {
			found = true
		}
	}
	if !found {
		r.watchedResources[key] = append(r.watchedResources[key], dependentResourceNsName)
		zap.S().Debugf("Watching %s to trigger reconciliation for %s", key, dependentResourceNsName)
	}
}

// unsafeRemoveWatchedResources stop watching resources with input namespace and watched type, if any.
// This function is not thread safe, use locking outside.
func (r *ResourceWatcher) unsafeRemoveWatchedResources(namespace string, wType Type, dependentResourceNsName types.NamespacedName) {
	for key := range r.watchedResources {
		if key.ResourceType == wType && key.Resource.Namespace == namespace {
			index := -1
			for i, v := range r.watchedResources[key] {
				if v == dependentResourceNsName {
					index = i
				}
			}

			if index == -1 {
				continue
			}

			zap.S().Infof("Removing %s from resources dependent on %s", dependentResourceNsName, key)

			if index == 0 {
				if len(r.watchedResources[key]) == 1 {
					delete(r.watchedResources, key)
					continue
				}
				r.watchedResources[key] = r.watchedResources[key][index+1:]
				continue
			}

			if index == len(r.watchedResources[key]) {
				r.watchedResources[key] = r.watchedResources[key][:index]
				continue
			}

			r.watchedResources[key] = append(r.watchedResources[key][:index], r.watchedResources[key][index+1:]...)
		}
	}
}

// RemoveAllDependentWatchedResources stop watching resources with input namespace and dependent resource
func (r *ResourceWatcher) RemoveAllDependentWatchedResources(namespace string, dependentResourceNsName types.NamespacedName) {
	r.mapLock.Lock()
	defer r.mapLock.Unlock()

	watchedResourceTypes := map[Type]bool{}
	for resource := range r.watchedResources {
		watchedResourceTypes[resource.ResourceType] = true
	}

	for resourceType := range watchedResourceTypes {
		r.unsafeRemoveWatchedResources(namespace, resourceType, dependentResourceNsName)
	}
}
