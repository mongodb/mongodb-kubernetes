package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"os"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	APP_LABEL_KEY = "app"
	// The label that defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	POD_ANTI_AFFINITY_LABEL_KEY = "pod-anti-affinity"
)

// PodVars is a convenience struct to pass environment variables to Pods as needed.
// They are used by the automation agent to connect to Ops/Cloud Manager.
type PodVars struct {
	BaseUrl     string
	ProjectId   string
	User        string
	AgentApiKey string
}

// buildStatefulSet builds the statefulset of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources.
// This is a convenience method to pass all attributes inside a "parameters" object which is easier to
// build in client code and avoid passing too many different parameters to `buildStatefulSet`.
func buildStatefulSet(p StatefulSetHelper) *appsv1.StatefulSet {
	labels := map[string]string{
		APP_LABEL_KEY:               p.Service,
		"controller":                util.OmControllerLabel,
		POD_ANTI_AFFINITY_LABEL_KEY: p.Name,
	}

	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            p.Name,
			Namespace:       p.Namespace,
			OwnerReferences: baseOwnerReference(p.Owner),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: p.Service,
			Replicas:    util.Int32Ref(int32(p.Replicas)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: basePodSpec(p.Name, p.PodSpec, p.PodVars),
			},
		},
	}
	// If 'persistent' flag is not set - we consider it to be true
	if p.Persistent == nil || *p.Persistent {
		buildPersistentVolumeClaims(set, p)
	}
	return set
}

func buildPersistentVolumeClaims(set *appsv1.StatefulSet, p StatefulSetHelper) {
	var claims []corev1.PersistentVolumeClaim
	var mounts []corev1.VolumeMount

	// if persistence not set or if single one is set
	if p.PodSpec.Persistence == nil || (p.PodSpec.Persistence.SingleConfig == nil && p.PodSpec.Persistence.MultipleConfig == nil) ||
		p.PodSpec.Persistence.SingleConfig != nil {
		var config *mongodb.PersistenceConfig
		if p.PodSpec.Persistence == nil || p.PodSpec.Persistence.SingleConfig == nil {
			config = createBackwordCompatibleConfig(p)
		} else {
			config = p.PodSpec.Persistence.SingleConfig
		}
		// Single claim, multiple mounts using this claim. Note, that we use "subpaths" in the volume to mount to different
		// physical folders
		claims, mounts = createClaimsAndMountsSingleMode(config, p)
	} else if p.PodSpec.Persistence.MultipleConfig != nil {
		defaultConfig := p.PodSpec.Default.Persistence.MultipleConfig

		// Multiple claims, multiple mounts. No subpaths are used and everything is mounted to the root of directory
		claims, mounts = createClaimsAndMontsMultiMode(p, defaultConfig)
	}
	set.Spec.VolumeClaimTemplates = claims
	set.Spec.Template.Spec.Containers[0].VolumeMounts = mounts
}

// buildSecret creates the secret object to store agent key. This secret is read directly by Automation Agent containers
func buildSecret(secretName string, namespace string, agentKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		StringData: map[string]string{util.OmAgentApiKey: agentKey}}
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

// basePodSpec creates the standard Pod definition which uses the database container for managing mongod/mongos
// instances. Parameters to the container will be passed as environment variables which values are contained
// in the PodVars structure.
func basePodSpec(statefulSetName string, reqs mongodb.PodSpecWrapper, podVars *PodVars) corev1.PodSpec {
	spec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            util.ContainerName,
				Image:           util.ReadEnvVarOrPanic(util.AutomationAgentImageUrl),
				ImagePullPolicy: corev1.PullPolicy(util.ReadEnvVarOrPanic(util.AutomationAgentImagePullPolicy)),
				Env:             baseEnvFrom(podVars),
				Ports:           []corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}},
				Resources: corev1.ResourceRequirements{
					// Setting limits only sets "requests" to the same value (but not vice versa)
					// This seems as a fair trade off as having these values different may result in incorrect wiredtiger
					// cache (e.g too small: it was configured for "request" memory size and then container
					// memory grew to "limit", too big: wired tiger cache was configured by "limit" by the real memory for
					// container is at "resource" values)
					Limits: buildRequirements(reqs),
				},
				LivenessProbe: baseLivenessProbe(),
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{{
			Name: os.Getenv(util.AutomationAgentPullSecrets),
		}},
		Affinity: &corev1.Affinity{
			NodeAffinity: reqs.NodeAffinity,
			PodAffinity:  reqs.PodAffinity,
		},
	}

	_, managedSecurityContext := util.ReadEnv(util.ManagedSecurityContextEnv)
	if !managedSecurityContext {
		if reqs.SecurityContext != nil {
			spec.SecurityContext = reqs.SecurityContext
		} else {
			spec.SecurityContext = &corev1.PodSecurityContext{
				// By default, containers will never run as root.
				// unless `MANAGED_SECURITY_CONTEXT` env variable is set, in which case the SecurityContext
				// should be managed by Kubernetes (this is the default in OpenShift)
				RunAsUser:    util.Int64Ref(util.RunAsUser),
				FSGroup:      util.Int64Ref(util.FsGroup),
				RunAsNonRoot: util.BooleanRef(true),
			}
		}
	}

	spec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
			// it to 100
			Weight: 100,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{POD_ANTI_AFFINITY_LABEL_KEY: statefulSetName}},
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
			Exec: &corev1.ExecAction{[]string{util.LivenessProbe}},
		},
		InitialDelaySeconds: 60,
		TimeoutSeconds:      30,
		PeriodSeconds:       30,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}
}

func baseEnvFrom(podVars *PodVars) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  util.ENV_VAR_BASE_URL,
			Value: podVars.BaseUrl,
		},
		{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: podVars.ProjectId,
		},
		{
			Name:  util.ENV_VAR_USER,
			Value: podVars.User,
		},
		{
			Name:  util.ENV_VAR_AGENT_API_KEY,
			Value: podVars.AgentApiKey,
		},
	}
}

func createClaimsAndMontsMultiMode(p StatefulSetHelper, defaultConfig *mongodb.MultiplePersistenceConfig) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	claims := []corev1.PersistentVolumeClaim{
		*pvc(util.PvcNameData, p.PodSpec.Persistence.MultipleConfig.Data, defaultConfig.Data),
		*pvc(util.PvcNameJournal, p.PodSpec.Persistence.MultipleConfig.Journal, defaultConfig.Journal),
		*pvc(util.PvcNameLogs, p.PodSpec.Persistence.MultipleConfig.Logs, defaultConfig.Logs),
	}
	mounts := []corev1.VolumeMount{
		*mount(util.PvcNameData, util.PvcMountPathData, ""),
		*mount(util.PvcNameJournal, util.PvcMountPathJournal, ""),
		*mount(util.PvcNameLogs, util.PvcMountPathLogs, ""),
	}
	return claims, mounts
}

func createClaimsAndMountsSingleMode(config *mongodb.PersistenceConfig, p StatefulSetHelper) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	claims := []corev1.PersistentVolumeClaim{*pvc(util.PvcNameData, config, p.PodSpec.Default.Persistence.SingleConfig)}
	mounts := []corev1.VolumeMount{
		*mount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		*mount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		*mount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	}
	return claims, mounts
}

func createBackwordCompatibleConfig(p StatefulSetHelper) *mongodb.PersistenceConfig {
	// backward compatibility: take storage and class values from old properties (if they are specified)
	if p.PodSpec.StorageClass == "" {
		return &mongodb.PersistenceConfig{Storage: p.PodSpec.Storage}
	} else {
		return &mongodb.PersistenceConfig{Storage: p.PodSpec.Storage, StorageClass: &p.PodSpec.StorageClass}
	}
}

func pvc(name string, config, defaultConfig *mongodb.PersistenceConfig) *corev1.PersistentVolumeClaim {
	claim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: buildStorageRequirements(config, defaultConfig),
			},
		}}
	if config != nil {
		claim.Spec.Selector = config.LabelSelector
		claim.Spec.StorageClassName = config.StorageClass
	}
	return &claim
}

func mount(name, path, subpath string) *corev1.VolumeMount {
	volumeMount := &corev1.VolumeMount{
		Name:      name,
		MountPath: path,
	}
	if subpath != "" {
		volumeMount.SubPath = subpath
	}
	return volumeMount
}
