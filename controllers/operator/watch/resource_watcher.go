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
	var res []types.NamespacedName
	for k := range r.WatchedResources {
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
func (r *ResourceWatcher) AddWatchedResourceIfNotAdded(name, namespace string,
	wType Type, dependentResourceNsName types.NamespacedName,
) {
	key := Object{
		ResourceType: wType,
		Resource: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	}
	if _, ok := r.WatchedResources[key]; !ok {
		r.WatchedResources[key] = make([]types.NamespacedName, 0)
	}
	found := false
	for _, v := range r.WatchedResources[key] {
		if v == dependentResourceNsName {
			found = true
		}
	}
	if !found {
		r.WatchedResources[key] = append(r.WatchedResources[key], dependentResourceNsName)
		zap.S().Debugf("Watching %s to trigger reconciliation for %s", key, dependentResourceNsName)
	}
}

// RemoveWatchedResources stop watching resources with input namespace and watched type, if any
func (r *ResourceWatcher) RemoveWatchedResources(namespace string, wType Type, dependentResourceNsName types.NamespacedName) {
	for key := range r.WatchedResources {
		if key.ResourceType == wType && key.Resource.Namespace == namespace {
			index := -1
			for i, v := range r.WatchedResources[key] {
				if v == dependentResourceNsName {
					index = i
				}
			}

			if index == -1 {
				continue
			}

			zap.S().Infof("Removing %s from resources dependent on %s", dependentResourceNsName, key)

			if index == 0 {
				if len(r.WatchedResources[key]) == 1 {
					delete(r.WatchedResources, key)
					continue
				}
				r.WatchedResources[key] = r.WatchedResources[key][index+1:]
				continue
			}

			if index == len(r.WatchedResources[key]) {
				r.WatchedResources[key] = r.WatchedResources[key][:index]
				continue
			}

			r.WatchedResources[key] = append(r.WatchedResources[key][:index], r.WatchedResources[key][index+1:]...)
		}
	}
}

// RemoveAllDependentWatchedResources stop watching resources with input namespace and dependent resource
func (r *ResourceWatcher) RemoveAllDependentWatchedResources(namespace string, dependentResourceNsName types.NamespacedName) {
	watchedResourceTypes := map[Type]bool{}
	for resource := range r.WatchedResources {
		watchedResourceTypes[resource.ResourceType] = true
	}

	for resourceType := range watchedResourceTypes {
		r.RemoveWatchedResources(namespace, resourceType, dependentResourceNsName)
	}
}
