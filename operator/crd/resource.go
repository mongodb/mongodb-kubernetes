package crd

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
)

type CustomResource struct {
	Name    string
	Plural  string
	Group   string
	Version string
	Scope   apiextensionsv1beta1.ResourceScope
	Kind    string
}

type ResourceWatcher struct {
	resource              CustomResource
	namespace             string
	resourceEventHandlers cache.ResourceEventHandlerFuncs
	client                rest.Interface
	scheme                *runtime.Scheme
}

type Context struct {
	Clientset             kubernetes.Interface
	APIExtensionClientset apiextensionsclient.Interface
	Interval              time.Duration
	Timeout               time.Duration
}

// NewWatcher creates an instance of a custom resource watcher for the given resource
func NewWatcher(resource CustomResource, namespace string, handlers cache.ResourceEventHandlerFuncs, client rest.Interface) *ResourceWatcher {
	return &ResourceWatcher{
		resource:              resource,
		namespace:             namespace,
		resourceEventHandlers: handlers,
		client:                client,
	}
}

func (w *ResourceWatcher) Watch(objType runtime.Object, done <-chan struct{}) error {
	source := cache.NewListWatchFromClient(
		w.client,
		w.resource.Plural,
		w.namespace,
		fields.Everything())
	_, controller := cache.NewInformer(
		source,
		objType,
		0,
		w.resourceEventHandlers)

	go controller.Run(done)
	<-done
	return nil
}

func BuildCustomResources(context Context, resources []CustomResource) error {
	var lastErr error
	for _, resource := range resources {
		err := BuildCustomResource(context, resource)
		if err != nil {
			lastErr = err
		}
	}

	return lastErr

}

func BuildCustomResource(context Context, resource CustomResource) error {
	crdName := fmt.Sprintf("%s.%s", resource.Plural, resource.Group)
	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   resource.Group,
			Version: resource.Version,
			Scope:   resource.Scope,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Singular: resource.Name,
				Plural:   resource.Plural,
				Kind:     resource.Kind,
			},
		},
	}

	_, err := context.APIExtensionClientset.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("Failed to create resource from %s. %+v", resource.Name, err)
		}
	}

	if err = waitCustomResourceInit(context, resource); err != nil {
		return err
	}

	return nil
}

func waitCustomResourceInit(context Context, resource CustomResource) error {
	crdName := fmt.Sprintf("%s.%s", resource.Plural, resource.Group)
	return wait.Poll(context.Interval, context.Timeout, func() (bool, error) {
		crd, err := context.APIExtensionClientset.ApiextensionsV1beta1().CustomResourceDefinitions().Get(crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		for _, cond := range crd.Status.Conditions {
			switch cond.Type {
			case apiextensionsv1beta1.Established:
				if cond.Status == apiextensionsv1beta1.ConditionTrue {
					return true, nil
				}
			case apiextensionsv1beta1.NamesAccepted:
				if cond.Status == apiextensionsv1beta1.ConditionFalse {
					return false, fmt.Errorf("Name conflict: %v", cond.Reason)
				}
			}
		}
		return false, nil
	})
}
