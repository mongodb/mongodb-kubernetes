package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"os"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	APP_LABEL_KEY = "app"
)

// buildStatefulSet builds the statefulset of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources
func buildStatefulSet(owner metav1.Object, name, serviceName, ns, configName, agentKeySecretName string, replicas int,
	persistent *bool, podSpec mongodb.PodSpecWrapper) *appsv1.StatefulSet {
	labels := map[string]string{
		APP_LABEL_KEY: serviceName,
		"controller":  OmControllerLabel,
	}

	set := appsv1.StatefulSet{
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
				Spec: basePodSpec(serviceName, configName, agentKeySecretName, persistent, podSpec),
			},
		},
	}
	// If 'persistent' flag is not set - we consider it to be true
	if persistent == nil || *persistent {
		set.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{
				Name: PersistentVolumeClaimName,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &podSpec.StorageClass,
				Resources: corev1.ResourceRequirements{
					Requests: buildStorageRequirements(podSpec),
				},
			},
		}}
	}
	return &set
}

// buildSecret creates the secret object to store agent key. This secret is read directly by Automation Agent containers
func buildSecret(groupId string, namespace string, agentKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      groupId,
			Namespace: namespace,
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
			Labels:          map[string]string{APP_LABEL_KEY: label},
			OwnerReferences: baseOwnerReference(owner),
		},
		Spec: corev1.ServiceSpec{
			Selector:  map[string]string{APP_LABEL_KEY: label},
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
func basePodSpec(serviceName, omConfigMapName, agentKeySecretName string, persistent *bool, reqs mongodb.PodSpecWrapper) corev1.PodSpec {
	spec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            ContainerName,
				Image:           os.Getenv(AutomationAgentImageUrl),
				ImagePullPolicy: corev1.PullPolicy(os.Getenv(AutomationAgentImagePullPolicy)),
				EnvFrom:         baseEnvFrom(omConfigMapName, agentKeySecretName),
				Ports:           []corev1.ContainerPort{{ContainerPort: MongoDbDefaultPort}},
				Resources: corev1.ResourceRequirements{
					Requests: buildRequirements(reqs),
				},
				SecurityContext: &corev1.SecurityContext{
					Privileged:   BooleanRef(false),
					RunAsNonRoot: BooleanRef(true),
				},
				LivenessProbe: baseLivenessProbe(),
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{{
			Name: os.Getenv(AutomationAgentPullSecrets),
		}},
		Affinity: &corev1.Affinity{
			NodeAffinity: reqs.NodeAffinity,
			PodAffinity:  reqs.PodAffinity,
		},
	}
	if persistent == nil || *persistent {
		spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name:      PersistentVolumeClaimName,
			MountPath: PersistentVolumePath,
		}}
	}
	spec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
			// it to 100
			Weight: 100,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{APP_LABEL_KEY: serviceName}},
				// If PodAntiAffinityTopologyKey config property is empty - then it's ok to use some default (even for standalones)
				TopologyKey: reqs.GetTopologyKeyOrDefault(),
			},
		}},
	}
	return spec
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
