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

// buildStatefulSet builds the statefulset of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources
func buildStatefulSet(owner metav1.Object, name, serviceName, ns, configName, agentKeySecretName string, replicas int, requirements mongodb.MongoDbRequirements) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        serviceName,
		"controller": OmControllerLabel,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ns,
			OwnerReferences: baseOwnerReference(owner),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: serviceName,
			Replicas:    Int32Ref(int32(replicas)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: basePodSpec(configName, agentKeySecretName, requirements),
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name: PersistentVolumeClaimName,
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &requirements.StorageClass,
					Resources: corev1.ResourceRequirements{
						Requests: buildStorageRequirements(requirements),
					},
				},
			}},
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
func buildService(owner metav1.Object, name string, label string, namespace string, port int32, exposeExternally bool) *corev1.Service {
	serviceType := corev1.ServiceTypeClusterIP
	clusterIp := "None"
	if exposeExternally {
		serviceType = corev1.ServiceTypeNodePort
		clusterIp = ""
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          map[string]string{"app": label},
			OwnerReferences: baseOwnerReference(owner),
		},
		Spec: corev1.ServiceSpec{
			Selector:  map[string]string{"app": label},
			Type:      serviceType,
			ClusterIP: clusterIp,
			Ports:     []corev1.ServicePort{{Port: port}},
		},
	}
}

func baseOwnerReference(owner metav1.Object) []metav1.OwnerReference {
	reflectType := ""
	switch owner.(type) {
	case *mongodb.MongoDbStandalone:
		reflectType = "MongoDbStandalone"
	case *mongodb.MongoDbReplicaSet:
		reflectType = "MongoDbReplicaSet"
	case *mongodb.MongoDbShardedCluster:
		reflectType = "MongoDbShardedCluster"
	}
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   mongodb.SchemeGroupVersion.Group,
			Version: mongodb.SchemeGroupVersion.Version,
			// TODO please fix this: for some reasons this statement returns empty string (it returns fine if we
			// take the initial object itself (n *mongodb.MongoDbStandalone for example) and get the type from it using
			// reflect.TypeOf(*n).Name(). I've no idea why we can pass *mongodb.MongoDbStandalone to the method accepting
			// owner metav1.Object (not owner *metav1.Object)
			//Kind:    reflect.TypeOf(owner).Name(),
			Kind: reflectType,
		}),
	}
}

// basePodSpec creates the standard pod definition which uses the automation agent container for managing mongod/mongos
// instances. Configuration data is read from the config map named "omConfigMapName" value
func basePodSpec(omConfigMapName, agentKeySecretName string, reqs mongodb.MongoDbRequirements) corev1.PodSpec {
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
				VolumeMounts: []corev1.VolumeMount{{
					Name:      PersistentVolumeClaimName,
					MountPath: PersistentVolumePath,
				}},
				Resources: corev1.ResourceRequirements{
					Requests: buildRequirements(reqs),
				},
				SecurityContext: &corev1.SecurityContext{
					Privileged:   boolP(false),
					RunAsNonRoot: boolP(true),
				},
				LivenessProbe: baseLivenessProbe(),
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{{
			Name: os.Getenv(AutomationAgentPullSecrets),
		}},
	}
}

func baseLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{[]string{LivenessProbe}},
		},
		InitialDelaySeconds: 60,
		TimeoutSeconds:      30,
		PeriodSeconds:       30,
		SuccessThreshold:    1,
		FailureThreshold:    6,
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

/*func getOrFormatServiceName(service, objName string) string {
	if service == "" {
		return objName + "-service"
	}
	return service
}*/
