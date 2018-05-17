package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"os"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// buildStandaloneStatefulSet returns a StatefulSet which is how MongoDB Standalone objects
// are mapped into Kubernetes objects.
func buildStandaloneStatefulSet(obj *mongodb.MongoDbStandalone, agentKeySecretName string) *appsv1.StatefulSet {
	serviceName := getOrFormatServiceName(obj.Spec.Service, obj.Name)
	labels := map[string]string{
		"app":        serviceName,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Name,
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
			ServiceName: serviceName,
			Replicas:    Int32Ref(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: basePodSpec(obj.Spec.OmConfigName, agentKeySecretName),
			},
		},
	}
}

// buildReplicaSetStatefulSet will return a StatefulSet definition, built on top of Pods.
func buildReplicaSetStatefulSet(obj *mongodb.MongoDbReplicaSet, agentKeySecretName string) *appsv1.StatefulSet {
	serviceName := getOrFormatServiceName(obj.Spec.Service, obj.Name)
	labels := map[string]string{
		"app":        serviceName,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Name,
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
			ServiceName: serviceName,
			Replicas:    Int32Ref(int32(obj.Spec.Members)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: basePodSpec(obj.Spec.OmConfigName, agentKeySecretName),
			},
		},
	}
}

// buildSecret creates the secret object to store agent key. This secret is read directly by Automation Agent containers
func buildSecret(groupId string, nameSpace string, agentKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupId,
			Namespace: nameSpace,
		},
		StringData: map[string]string{AgentKey: agentKey}}
}

// buildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable
func buildService(name string, label string, nameSpace string, port int32, exposeExternally bool) *corev1.Service {
	serviceType := corev1.ServiceTypeClusterIP
	clusterIp := "None"
	if exposeExternally {
		serviceType = corev1.ServiceTypeNodePort
		clusterIp = ""
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: nameSpace,
			Labels:    map[string]string{"app": label},
			// We mark the service as created by Om Operator to distinct from the services made by the user
			Annotations: map[string]string{CreatedByOperator: "true"},
		},
		Spec: corev1.ServiceSpec{
			Selector:  map[string]string{"app": label},
			Type:      serviceType,
			ClusterIP: clusterIp,
			Ports:     []corev1.ServicePort{{Port: port}},
		},
	}
}

// basePodSpec creates the standard pod definition which uses the automation agent container for managing mongod/mongos
// instances. Configuration data is read from the config map named "omConfigMapName" value
func basePodSpec(omConfigMapName, agentKeySecretName string) corev1.PodSpec {
	boolP := func(v bool) *bool {
		return &v
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            ContainerName,
				Image:           os.Getenv(AutomationAgentImageUrl),
				ImagePullPolicy: corev1.PullPolicy(os.Getenv(AutomationAgentImagePullPolicy)),
				EnvFrom:         baseEnvFrom(omConfigMapName, agentKeySecretName),
				Ports:           []corev1.ContainerPort{{ContainerPort: 27017}},
				SecurityContext: &corev1.SecurityContext{
					Privileged:   boolP(false),
					RunAsNonRoot: boolP(true),
				},
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{{
			Name: os.Getenv(AutomationAgentPullSecrets),
		}},
	}
}

func baseEnvFrom(omConfigMapName, agentSecretName string) []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: omConfigMapName,
				},
			},
		},
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: agentSecretName,
				},
			},
		},
	}
}

func getOrFormatServiceName(service, objName string) string {
	if service == "" {
		return objName + "-service"
	}
	return service
}
