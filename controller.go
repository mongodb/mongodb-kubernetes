// Package main for a sample operator
package main

import (
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	mongodbscheme "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/scheme"
	mongodbclient "github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned/typed/mongodb.com/v1alpha1"
	opkit "github.com/rook/operator-kit"
	appsv1beta2 "k8s.io/api/apps/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
)

type MongoDbController struct {
	context          *opkit.Context
	mongodbClientset mongodbclient.MongodbV1alpha1Interface
}

func newMongoDbController(context *opkit.Context, mongodbClientset mongodbclient.MongodbV1alpha1Interface) *MongoDbController {
	mongodbscheme.AddToScheme(scheme.Scheme)

	return &MongoDbController{
		context:          context,
		mongodbClientset: mongodbClientset,
	}
}

func newDeployment(obj *mongodb.MongoDbReplicaSet) *appsv1beta2.Deployment {
	labels := map[string]string{
		"app":        "om-controller",
		"controller": "om-controller",
	}
	fmt.Printf("Getting something to newDeployment (members) '%d'", obj.Spec.Members)

	return &appsv1beta2.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.Name,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    "MongoDbReplicaSet",
				}),
			},
		},
		Spec: appsv1beta2.DeploymentSpec{
			Replicas: obj.Spec.Members,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "ops-manager-agent",
							Image:           "ops-manager-agent",
							ImagePullPolicy: "Never",
							EnvFrom: []corev1.EnvFromSource{
								{
									ConfigMapRef: &corev1.ConfigMapEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "ops-manager-config",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Watch watches for instances of MongoDbReplicaSet custom resources and acts on them
func (c *MongoDbController) StartWatch(namespace string, stopCh chan struct{}) error {

	resourceHandlers := cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdd,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	}
	restClient := c.mongodbClientset.RESTClient()

	// mongodb.MongoDbReplicaSetResource -> in v1alpha1/register.go
	watcher := opkit.NewWatcher(mongodb.MongoDbReplicaSetResource, namespace, resourceHandlers, restClient)

	go watcher.Watch(&mongodb.MongoDbReplicaSet{}, stopCh)
	return nil
}

func (c *MongoDbController) onAdd(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Println("So we got to onAdd... let's see if we can create some resource")

	deployment, err := c.context.Clientset.Apps().Deployments(s.Namespace).Create(newDeployment(s))
	if err != nil {
		fmt.Printf("Error while creating the deployment\n")
		fmt.Println(err)
	} else {
		fmt.Printf("Created deployment with %d replicas", *deployment.Spec.Replicas)
	}

	// fmt.Printf("Added MongoDbReplicaSet '%s' with members=%d\n", s.Name, s.Spec.Members)
}

func (c *MongoDbController) onUpdate(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Updated MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, oldRes.Spec.Members, newRes.Spec.Members)
}

func (c *MongoDbController) onDelete(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, s.Spec.Members)
}
