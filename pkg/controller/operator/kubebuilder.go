package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"path"
	"sort"
	"strconv"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	AppLabelKey = "app"
	// The label that defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	PodAntiAffinityLabelKey = "pod-anti-affinity"

	// SecretVolumeMountPath defines where in the Pod will be the secrets
	// object mounted.
	SecretVolumeMountPath = "/var/lib/mongodb-automation/secrets/certs"

	// SecretVolumeName is the name of the volume resource.
	SecretVolumeName = "secret-certs"

	// SecretVolumeCAMountPath defines where in the Pod will be the secrets
	// object mounted.
	SecretVolumeCAMountPath = "/var/lib/mongodb-automation/secrets/ca"

	// SecretVolumeCAName is the name of the volume resource.
	SecretVolumeCAName = "secret-ca"

	// CaCertMountPath defines where in the Pod the ca cert will be mounted.
	CaCertMountPath = "/mongodb-automation/certs"

	// AgentCertMountPath defines where in the Pod the ca cert will be mounted.
	AgentCertMountPath = "/mongodb-automation/" + util.AgentSecretName

	// CaCertMMS is the name of the CA file provided for MMS.
	CaCertMMS = "mms-ca.crt"

	// CaCertVolumeName is the name of the volume with the CA Cert
	CaCertName = "ca-cert-volume"

	// AgentLibPath defines the base path for agent configuration files including the automation
	// config file for the headless agent,
	AgentLibPath = "/var/lib/mongodb-automation/"

	// ClusterConfigVolumeName is the name of the volume resource.
	ClusterConfigVolumeName = "cluster-config"
)

// PodVars is a convenience struct to pass environment variables to Pods as needed.
// They are used by the automation agent to connect to Ops/Cloud Manager.
type PodVars struct {
	BaseURL     string
	ProjectID   string
	User        string
	AgentAPIKey string
	LogLevel    mdbv1.LogLevel

	// Related to MMS SSL configuration
	mdbv1.SSLProjectConfig
}

// createDatabaseStatefulSet is a general function for building the database StatefulSet.
// Reused for building an appdb StatefulSet and a normal mongodb StatefulSet
func createDatabaseStatefulSet(p StatefulSetHelper, podSpec corev1.PodSpec) *appsv1.StatefulSet {
	// ssLabels are labels we set to the StatefulSet
	ssLabels := map[string]string{
		AppLabelKey: p.Service,
	}
	// podLabels are labels we set to StatefulSet Selector and Template.Meta
	podLabels := map[string]string{
		AppLabelKey:             p.Service,
		"controller":            util.OmControllerLabel,
		PodAntiAffinityLabelKey: p.Name,
	}

	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            p.Name,
			Namespace:       p.Namespace,
			Labels:          ssLabels,
			OwnerReferences: baseOwnerReference(p.Owner),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: p.Service,
			Replicas:    util.Int32Ref(int32(p.Replicas)),
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: podSpec,
			},
		},
	}
	// If 'persistent' flag is not set - we consider it to be true
	if p.Persistent == nil || *p.Persistent {
		buildPersistentVolumeClaims(set, p)
	}

	mountVolumes(set, p)

	return set
}

// buildStatefulSet builds the StatefulSet of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources.
// This is a convenience method to pass all attributes inside a "parameters" object which is easier to
// build in client code and avoid passing too many different parameters to `buildStatefulSet`.
func buildStatefulSet(p StatefulSetHelper) *appsv1.StatefulSet {
	return createDatabaseStatefulSet(p, basePodSpec(p.Name, p.PodSpec, p.PodVars))
}

// buildAppDbStatefulSet builds the StatefulSet for AppDB.
// It's mostly the same as the normal Mongodb one but had a different pod spec and an additional mounting volume
func buildAppDbStatefulSet(p StatefulSetHelper) *appsv1.StatefulSet {
	set := createDatabaseStatefulSet(p, baseAppDbPodSpec(p.Name, p.PodSpec, p.Version))

	// cluster config mount
	mountVolume(volumeMountData{
		volumeMountName:  ClusterConfigVolumeName,
		volumeMountPath:  AgentLibPath,
		volumeName:       ClusterConfigVolumeName,
		volumeSourceType: corev1.ConfigMapVolumeSource{},
		volumeSourceName: p.Name + "-config",
	}, set)

	return set
}

// TODO refactor - merge with 'buildStatefulSet'
func buildOpsManagerStatefulSet(p OpsManagerStatefulSetHelper) *appsv1.StatefulSet {
	labels := map[string]string{
		AppLabelKey:             p.Service,
		"controller":            util.OmControllerLabel,
		PodAntiAffinityLabelKey: p.Name,
	}

	if p.IsBackupDaemon {
		p.EnvVars = append(p.EnvVars, corev1.EnvVar{
			// For the OM Docker image to run as Backup Daemon, the BACKUP_DAEMON env variable
			// needs to be passed with any value.
			Name:  "BACKUP_DAEMON",
			Value: "backup",
		})
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
				Spec: opsManagerPodSpec(p.EnvVars, p.Version),
			},
		},
	}

	if p.IsBackupDaemon {
		// backup daemon needs head db, this is set to some default value for now.
		// `kind` only supports a "default" storageclass
		defaultConfig := &mdbv1.PersistenceConfig{
			Storage:      p.Storage,
			StorageClass: &p.StorageClass,
		}
		set.Spec.VolumeClaimTemplates = append(
			set.Spec.VolumeClaimTemplates,
			*pvc("head", defaultConfig, defaultConfig),
		)
		set.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			set.Spec.Template.Spec.Containers[0].VolumeMounts,
			*mount("head", "/head", ""),
		)
	}

	mountVolume(volumeMountData{
		volumeMountName:  "gen-key",
		volumeMountPath:  util.GenKeyPath,
		volumeName:       "gen-key",
		volumeSourceType: corev1.SecretVolumeSource{},
		volumeSourceName: p.Owner.GetName() + "-gen-key",
	}, set)

	return set
}

// volumeMountData is a wrapper around all the fields required to
// mount a volume.
type volumeMountData struct {
	volumeMountName  string // TODO remove - it's always equal to the volume name
	volumeMountPath  string
	volumeName       string
	volumeSourceType interface{}
	volumeSourceName string
}

func mountVolume(mountData volumeMountData, set *appsv1.StatefulSet) {
	volMount := corev1.VolumeMount{
		Name:      mountData.volumeMountName,
		ReadOnly:  true,
		MountPath: mountData.volumeMountPath,
	}

	var vol corev1.Volume
	switch mountData.volumeSourceType.(type) {
	case corev1.ConfigMapVolumeSource:
		vol = corev1.Volume{
			Name: mountData.volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: mountData.volumeSourceName,
					},
				},
			},
		}
	case corev1.SecretVolumeSource:
		vol = corev1.Volume{
			Name: mountData.volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: mountData.volumeSourceName,
				},
			},
		}
	default:
		panic("unrecognized volumeSource type. Must be either ConfigMapVolumeSource or SecretVolumeSource")
	}

	set.Spec.Template.Spec.Containers[0].VolumeMounts =
		append(set.Spec.Template.Spec.Containers[0].VolumeMounts,
			volMount)
	set.Spec.Template.Spec.Volumes =
		append(set.Spec.Template.Spec.Volumes,
			vol)
}

// mountVolumes will add VolumeMounts to the `set` object.
// Make sure you keep this updated with `kubehelper.needToPublishStateFirst` as it declares
// in which order to make changes to StatefulSet and Ops Manager automationConfig
func mountVolumes(set *appsv1.StatefulSet, helper StatefulSetHelper) {
	// SSL is active
	if helper.Security != nil && helper.Security.TLSConfig.Enabled {
		tlsConfig := helper.Security.TLSConfig
		secretName := fmt.Sprintf("%s-cert", helper.Name)

		mountVolume(volumeMountData{
			volumeMountName:  SecretVolumeName,
			volumeMountPath:  SecretVolumeMountPath,
			volumeName:       SecretVolumeName,
			volumeSourceType: corev1.SecretVolumeSource{},
			volumeSourceName: secretName,
		}, set)

		if tlsConfig.CA != "" {
			mountVolume(volumeMountData{
				volumeMountName:  SecretVolumeCAName,
				volumeMountPath:  SecretVolumeCAMountPath,
				volumeName:       SecretVolumeCAName,
				volumeSourceType: corev1.ConfigMapVolumeSource{},
				volumeSourceName: tlsConfig.CA,
			}, set)
		}
	}

	if helper.PodVars.SSLMMSCAConfigMap != "" {
		mountVolume(volumeMountData{
			volumeMountName:  CaCertName,
			volumeMountPath:  CaCertMountPath,
			volumeName:       CaCertName,
			volumeSourceType: corev1.ConfigMapVolumeSource{},
			volumeSourceName: helper.PodVars.SSLMMSCAConfigMap,
		}, set)
	}

	if helper.Security != nil {
		if util.ContainsString(helper.Security.Authentication.Modes, util.X509) {
			mountVolume(volumeMountData{
				volumeMountName:  util.AgentSecretName,
				volumeMountPath:  AgentCertMountPath,
				volumeName:       util.AgentSecretName,
				volumeSourceType: corev1.SecretVolumeSource{},
				volumeSourceName: util.AgentSecretName,
			}, set)

			// add volume for x509 cert used in internal cluster authentication
			if helper.Security.Authentication.InternalCluster == util.X509 {
				mountVolume(volumeMountData{
					volumeMountName:  util.ClusterFileName,
					volumeMountPath:  util.InternalClusterAuthMountPath,
					volumeName:       util.ClusterFileName,
					volumeSourceType: corev1.SecretVolumeSource{},
					volumeSourceName: toInternalClusterAuthName(helper.Name),
				}, set)
			}
		}
	}
}

func buildPersistentVolumeClaims(set *appsv1.StatefulSet, p StatefulSetHelper) {
	var claims []corev1.PersistentVolumeClaim
	var mounts []corev1.VolumeMount

	// if persistence not set or if single one is set
	if p.PodSpec.Persistence == nil ||
		(p.PodSpec.Persistence.SingleConfig == nil && p.PodSpec.Persistence.MultipleConfig == nil) ||
		p.PodSpec.Persistence.SingleConfig != nil {
		var config *mdbv1.PersistenceConfig
		if p.PodSpec.Persistence != nil && p.PodSpec.Persistence.SingleConfig != nil {
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

	set.Spec.VolumeClaimTemplates = append(set.Spec.VolumeClaimTemplates, claims...)
	set.Spec.Template.Spec.Containers[0].VolumeMounts = append(set.Spec.Template.Spec.Containers[0].VolumeMounts, mounts...)
}

// buildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
func buildService(service *corev1.Service, namespacedName types.NamespacedName, owner Updatable, label string, port int32, exposeExternally bool) {
	serviceType := corev1.ServiceTypeClusterIP
	publishNotReady := true
	if exposeExternally {
		serviceType = corev1.ServiceTypeNodePort
		publishNotReady = false
	}

	if service == nil {
		service = &corev1.Service{}
	}

	servicePort := corev1.ServicePort{Port: port}
	if !exposeExternally {
		servicePort.Name = "mongodb"
	} else if len(service.Spec.Ports) > 0 {
		// If the service exists and has a nodeport specified - we copy the value
		servicePort.NodePort = service.Spec.Ports[0].NodePort
	}

	// Each attribute needs to be set manually to avoid overwritting or deleting
	// attributes from the subobject that we don't know about.
	service.ObjectMeta.Name = namespacedName.Name
	service.ObjectMeta.Namespace = namespacedName.Namespace
	service.ObjectMeta.Labels = map[string]string{AppLabelKey: label}
	service.ObjectMeta.OwnerReferences = baseOwnerReference(owner)

	service.Spec.Selector = map[string]string{AppLabelKey: label}
	service.Spec.Type = serviceType
	service.Spec.Ports = []corev1.ServicePort{servicePort}

	if !exposeExternally {
		service.Spec.ClusterIP = "None"
	}

	// We publish this address even when it is not ready, so it can join the party!
	service.Spec.PublishNotReadyAddresses = publishNotReady
}

func baseOwnerReference(owner Updatable) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   mdbv1.SchemeGroupVersion.Group,
			Version: mdbv1.SchemeGroupVersion.Version,
			Kind:    owner.GetKind(),
		}),
	}
}

// createBaseDbPodSpec is a base pod spec build for both AppDB and MongoDB
func createBaseDbPodSpec(container corev1.Container, statefulSetName string, reqs mdbv1.PodSpecWrapper) corev1.PodSpec {
	spec := corev1.PodSpec{
		Containers: []corev1.Container{container},
		Affinity: &corev1.Affinity{
			NodeAffinity: reqs.NodeAffinity,
			PodAffinity:  reqs.PodAffinity,
		},
		TerminationGracePeriodSeconds: util.Int64Ref(util.DefaultPodTerminationPeriodSeconds),
	}

	if val, found := util.ReadEnv(util.AutomationAgentPullSecrets); found {
		spec.ImagePullSecrets = []corev1.LocalObjectReference{{
			Name: val,
		}}
	}

	ensurePodSecurityContext(reqs.SecurityContext, &spec)

	spec.Affinity.PodAntiAffinity = &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			// Weight thoughts - seems no other affinity rule should be stronger than anti affinity one so putting
			// it to 100
			Weight: 100,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{PodAntiAffinityLabelKey: statefulSetName}},
				// If PodAntiAffinityTopologyKey config property is empty - then it's ok to use some default (even for standalones)
				TopologyKey: reqs.GetTopologyKeyOrDefault(),
			},
		}},
	}

	return spec
}

// ensurePodSecurityContext adds the 'SecurityContext' to the pod spec if it's necessary (Openshift doesn't need this
// as it manages the security by itself)
func ensurePodSecurityContext(explicitContext *corev1.PodSecurityContext, spec *corev1.PodSpec) {
	managedSecurityContext, _ := util.ReadBoolEnv(util.ManagedSecurityContextEnv)
	if !managedSecurityContext {
		if explicitContext != nil {
			spec.SecurityContext = explicitContext
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
}

// basePodSpec creates the standard Pod definition which uses the database container for managing mongod/mongos
// instances. Parameters to the container will be passed as environment variables which values are contained
// in the PodVars structure.
func basePodSpec(statefulSetName string, reqs mdbv1.PodSpecWrapper, podVars *PodVars) corev1.PodSpec {
	container := corev1.Container{
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
			Limits:   buildLimitsRequirements(reqs),
			Requests: buildRequestsRequirements(reqs),
		},
		LivenessProbe:  baseLivenessProbe(),
		ReadinessProbe: baseReadinessProbe(),
	}
	return createBaseDbPodSpec(container, statefulSetName, reqs)
}

// baseAppDbPodSpec creates the AppDB pod template. The container spec is mostly the same as for the MongoDB one -
// just different url and readiness probe
func baseAppDbPodSpec(statefulSetName string, reqs mdbv1.PodSpecWrapper, version string) corev1.PodSpec {
	appdbImageUrl := fmt.Sprintf("%s:%s", util.ReadEnvVarOrPanic(util.AppDBImageUrl), version)
	container := corev1.Container{
		Name:            util.ContainerAppDbName,
		Image:           appdbImageUrl,
		ImagePullPolicy: corev1.PullPolicy(util.ReadEnvVarOrPanic(util.AutomationAgentImagePullPolicy)),
		Env:             appdbContainerEnv(statefulSetName),
		Ports:           []corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort}},
		Resources: corev1.ResourceRequirements{
			Limits:   buildLimitsRequirements(reqs),
			Requests: buildRequestsRequirements(reqs),
		},
		LivenessProbe:  baseLivenessProbe(),
		ReadinessProbe: baseAppDbReadinessProbe(),
	}

	podSpec := createBaseDbPodSpec(container, statefulSetName, reqs)

	// AppDB must run under a dedicated account with special readConfigMap permissions
	podSpec.ServiceAccountName = util.AppDBServiceAccount
	return podSpec
}

func appdbContainerEnv(statefulSetName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:      util.ENV_POD_NAMESPACE,
			ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
		},
		{
			Name: util.ENV_AUTOMATION_CONFIG_MAP,
			// not critical but would be nice to reuse `AppDB.AutomationConfigSecretName`
			Value: statefulSetName + "-config",
		},
		{
			Name:  util.ENV_HEADLESS_AGENT,
			Value: "true",
		},
	}
}

func opsManagerPodSpec(envVars []corev1.EnvVar, version string) corev1.PodSpec {
	// TODO memory must be a configurable parameter (must also affect the JVM parameters for starting the OM instance)
	// let's have it hardcoded for alpha
	var defaultMemory resource.Quantity
	if q := parseQuantityOrZero("5G"); !q.IsZero() {
		defaultMemory = q
	}

	sort.Sort(&envVarSorter{envVars: envVars})
	omImageUrl := fmt.Sprintf("%s:%s", util.ReadEnvVarOrPanic(util.OpsManagerImageUrl), version)
	spec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            util.OpsManagerName,
				Image:           omImageUrl,
				ImagePullPolicy: corev1.PullPolicy(util.ReadEnvVarOrPanic(util.OpsManagerPullPolicy)),
				Env:             envVars,
				Ports:           []corev1.ContainerPort{{ContainerPort: util.OpsManagerDefaultPort}},
				Resources: corev1.ResourceRequirements{
					// Setting limits only sets "requests" to the same value (but not vice versa)
					Limits: corev1.ResourceList{corev1.ResourceMemory: defaultMemory},
				},
			},
		},
	}

	ensurePodSecurityContext(nil, &spec)

	return spec
}

// envVarSorter
type envVarSorter struct {
	envVars []corev1.EnvVar
}

// Len is part of sort.Interface.
func (s *envVarSorter) Len() int {
	return len(s.envVars)
}

// Swap is part of sort.Interface.
func (s *envVarSorter) Swap(i, j int) {
	s.envVars[i], s.envVars[j] = s.envVars[j], s.envVars[i]
}

// Less is part of sort.Interface. It is implemented by calling the "by" closure in the sorter.
func (s *envVarSorter) Less(i, j int) bool {
	return s.envVars[i].Name < s.envVars[j].Name
}

func baseLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.LivenessProbe}},
		},
		InitialDelaySeconds: 60,
		TimeoutSeconds:      30,
		PeriodSeconds:       30,
		SuccessThreshold:    1,
		FailureThreshold:    6,
	}
}

// opsManagerReadinessProbe creates the readiness probe
// TODO: This is disabled currently because of one weird aspect: if the readiness probe reports false
// while the container is starting - this results in container restart.
// So to avoid false restarts we need to set 'InitialDelaySeconds' very high.
// This however will affect the `kubehelper#waitForStatefulsetAndPods` as it will hang for too long
// because we check for 'set.Status.ReadyReplicas'
// so far we'll just manually check 8080 port from the Operator to check when the OM instance is ready
//func opsManagerReadinessProbe() *corev1.Probe {
//return &corev1.Probe{
//Handler: corev1.Handler{
//HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080), Path: "/"},
//},
//InitialDelaySeconds: 120,
//TimeoutSeconds:      5,
//PeriodSeconds:       10,
//SuccessThreshold:    1,
//FailureThreshold:    18, // So the probe will fail after ~3 minutes of Ops Manager being non-responsive
//}
//}

func baseReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.ReadinessProbe}},
		},
		// Setting the failure threshold to quite big value as the agent may spend some time to reach the goal
		FailureThreshold: 240,
		// The agent may be not on time to write the status file right after the container is created - we need to wait
		// for some time
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
	}
}
func baseAppDbReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		Handler: corev1.Handler{
			Exec: &corev1.ExecAction{Command: []string{util.ReadinessProbe}},
		},
		// Need to set to 1 to make readiness "interactive" and to indicate whether the agent has reached the goal or not
		FailureThreshold: 1,
		// The agent may be not on time to write the status file right after the container is created - we need to wait
		// for some time (todo check this)
		InitialDelaySeconds: 5,
		// We need more frequent check to provide faster response to the Operator
		PeriodSeconds: 5,
	}
}

func baseEnvFrom(podVars *PodVars) []corev1.EnvVar {
	vars := []corev1.EnvVar{
		{
			Name:  util.ENV_VAR_BASE_URL,
			Value: podVars.BaseURL,
		},
		{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: podVars.ProjectID,
		},
		{
			Name:  util.ENV_VAR_USER,
			Value: podVars.User,
		},
		{
			Name:  util.ENV_VAR_AGENT_API_KEY,
			Value: podVars.AgentAPIKey,
		},
		{
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: string(podVars.LogLevel),
		},
	}

	trustedCACertLocation := ""
	if podVars.SSLRequireValidMMSServerCertificates {
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLRequireValidMMSCertificates,
				Value: strconv.FormatBool(podVars.SSLRequireValidMMSServerCertificates),
			},
		)
	}

	if podVars.SSLMMSCAConfigMap != "" {
		// A custom CA has been provided, point the trusted CA to the location of custom CAs
		// trustedCACertLocation = util.
		trustedCACertLocation = path.Join(CaCertMountPath, CaCertMMS)

		vars = append(vars,
			corev1.EnvVar{
				// This points to the location of the mms-ca.crt in the mounted volume
				// It will be mounted during Pod creation.
				Name:  util.EnvVarSSLTrustedMMSServerCertificate,
				Value: util.SSLMMSCALocation,
			},
		)
	}

	// TODO(rodrigo): BUG here, the same env variable will be set twice with different values.
	if trustedCACertLocation != "" {
		// The value of this variable depends on 2 things:
		// If the user sets "require valid" we expect it to be based on the KubeCA
		// If the user provides its own CA, it will be based on this CA.
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLTrustedMMSServerCertificate,
				Value: trustedCACertLocation,
			},
		)

	}

	return vars
}

func createClaimsAndMontsMultiMode(p StatefulSetHelper, defaultConfig *mdbv1.MultiplePersistenceConfig) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
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

func createClaimsAndMountsSingleMode(config *mdbv1.PersistenceConfig, p StatefulSetHelper) ([]corev1.PersistentVolumeClaim, []corev1.VolumeMount) {
	claims := []corev1.PersistentVolumeClaim{*pvc(util.PvcNameData, config, p.PodSpec.Default.Persistence.SingleConfig)}
	mounts := []corev1.VolumeMount{
		*mount(util.PvcNameData, util.PvcMountPathData, util.PvcNameData),
		*mount(util.PvcNameData, util.PvcMountPathJournal, util.PvcNameJournal),
		*mount(util.PvcNameData, util.PvcMountPathLogs, util.PvcNameLogs),
	}
	return claims, mounts
}

// pvc convenience function to build a PersistentVolumeClaim.
//
// TODO: Describe why this function receives 2 "configs"
func pvc(name string, config, defaultConfig *mdbv1.PersistenceConfig) *corev1.PersistentVolumeClaim {
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

// mount convenience function to build a VolumeMount.
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
