package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"fmt"
)

// buildStandaloneStatefulSet returns a StatefulSet which is how MongoDB Standalone objects
// are mapped into Kubernetes objects.
func buildStandaloneStatefulSet(obj *mongodb.MongoDbStandalone, agentKeySecretName string) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        LabelApp,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.HostnamePrefix,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    MongoDbStandalone,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: MakeIntReference(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: baseContainer(obj.Name, agentKeySecretName),
			},
		},
	}
}

// buildReplicaSetStatefulSet will return a StatefulSet definition, built on top of Pods.
func buildReplicaSetStatefulSet(obj *mongodb.MongoDbReplicaSet, agentKeySecretName string) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        obj.Spec.Service,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.Name,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    MongoDbReplicaSet,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: obj.Spec.Service,
			Replicas:    &obj.Spec.Members,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: baseContainer(obj.Spec.HostnamePrefix, agentKeySecretName),
			},
		},
	}
}

func buildSecret(groupId string, nameSpace string, agentKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupId,
			Namespace: nameSpace,
		},
		StringData: map[string]string{AgentKey: agentKey}}
}

func baseContainer(name string, agentKeySecretName string) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            ContainerName,
				Image:           ContainerImage,
				ImagePullPolicy: ContainerImagePullPolicy,
				EnvFrom:         baseEnvFrom(agentKeySecretName),
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: 27017,
						Name:          name,
					},
				},
			},
		},
	}
}

func baseEnvFrom(agentSecretName string) []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: ContainerConfigMapName,
				},
			},
		},
		{
			SecretRef: &corev1.SecretEnvSource {
				LocalObjectReference: corev1.LocalObjectReference{
					Name: agentSecretName,
				},
			},
		},
	}
}
