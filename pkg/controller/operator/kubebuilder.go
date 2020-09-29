package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"path"
	"strconv"
	"strings"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"k8s.io/apimachinery/pkg/api/resource"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	AppLabelKey = "app"
	// PodAntiAffinityLabelKey defines the anti affinity rule label. The main rule is to spread entities inside one statefulset
	// (aka replicaset) to different locations, so pods having the same label shouldn't coexist on the node that has
	// the same topology key
	PodAntiAffinityLabelKey = "pod-anti-affinity"

	// ConfigMapVolumeCAName is the name of the volume used to mount CA certs
	ConfigMapVolumeCAName = "secret-ca"

	// CaCertMountPath defines where in the Pod the ca cert will be mounted.
	CaCertMountPath = "/mongodb-automation/certs"

	// CaCertName is the name of the volume with the CA Cert
	CaCertName = "ca-cert-volume"

	OneMB = 1048576

	OpsManagerPodMemPercentage = 90
)

// PodEnvVars is a convenience struct to pass environment variables to Pods as needed.
// They are used by the automation agent to connect to Ops/Cloud Manager.
type PodEnvVars struct {
	BaseURL     string
	ProjectID   string
	User        string
	AgentAPIKey string
	LogLevel    mdbv1.LogLevel

	// Related to MMS SSL configuration
	mdbv1.SSLProjectConfig
}

// buildStatefulSet builds the StatefulSet of pods containing agent containers. It's a general function used by
// all the types of mongodb deployment resources.
// This is a convenience method to pass all attributes inside a "parameters" object which is easier to
// build in client code and avoid passing too many different parameters to `buildStatefulSet`.
func buildStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	sts := construct.DatabaseStatefulSet(p)
	if p.PodSpec != nil && p.PodSpec.PodTemplate != nil {
		return enterprisests.MergeSpec(sts, &appsv1.StatefulSetSpec{Template: *p.PodSpec.PodTemplate})
	}
	return sts, nil
}

// buildAppDbStatefulSet builds the StatefulSet for AppDB.
// It's mostly the same as the normal MongoDB one but has slightly different container and an additional mounting volume
func buildAppDbStatefulSet(p StatefulSetHelper) (appsv1.StatefulSet, error) {
	sts := construct.AppDbStatefulSet(p)
	// This merge should be included in the above function call
	if p.PodSpec != nil && p.PodSpec.PodTemplate != nil {
		return enterprisests.MergeSpec(sts, &appsv1.StatefulSetSpec{Template: *p.PodSpec.PodTemplate})
	}
	return sts, nil
}

// buildOpsManagerStatefulSet builds the StatefulSet for Ops Manager
func buildOpsManagerStatefulSet(p OpsManagerStatefulSetHelper) (appsv1.StatefulSet, error) {
	sts := construct.OpsManagerStatefulSet(p)
	var err error
	if p.StatefulSetConfiguration != nil {
		sts, err = enterprisests.MergeSpec(sts, &p.StatefulSetConfiguration.Spec)
		if err != nil {
			return appsv1.StatefulSet{}, nil
		}
	}
	if err = setJvmArgsEnvVars(p.Spec, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}

	return sts, nil
}

// setJvmArgsEnvVars sets the correct environment variables for JVM size parameters.
// This method must be invoked on the final version of the StatefulSet (after user statefulSet spec
// was merged)
func setJvmArgsEnvVars(m omv1.MongoDBOpsManagerSpec, sts *appsv1.StatefulSet) error {
	jvmParamsEnvVars, err := buildJvmParamsEnvVars(m, sts.Spec.Template)
	if err != nil {
		return err
	}
	// pass Xmx java parameter to container (note, that we don't need to sort the env variables again
	// as the jvm params order is consistent)
	for _, envVar := range jvmParamsEnvVars {
		sts.Spec.Template.Spec.Containers[0].Env = append(sts.Spec.Template.Spec.Containers[0].Env, envVar)
	}
	return nil
}

// buildBackupDaemonStatefulSet builds the StatefulSet for backup daemon. It shares most of the configuration with
// OpsManager StatefulSet adding something on top of it
func buildBackupDaemonStatefulSet(p BackupStatefulSetHelper) (appsv1.StatefulSet, error) {
	sts := construct.BackupStatefulSet(p)
	var err error
	if p.StatefulSetConfiguration != nil {
		sts, err = enterprisests.MergeSpec(sts, &p.StatefulSetConfiguration.Spec)
		if err != nil {
			return appsv1.StatefulSet{}, nil
		}
	}
	// We need to calculate JVM memory parameters after the StatefulSet is merged
	// One idea for future: we can use the functional approach instead of Builder for Statefulset
	// and jvm mutation callbacks to the builder
	if err := setJvmArgsEnvVars(p.Spec, &sts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return sts, nil
}
func buildJvmParamsEnvVars(m omv1.MongoDBOpsManagerSpec, template corev1.PodTemplateSpec) ([]corev1.EnvVar, error) {
	mmsJvmEnvVar := corev1.EnvVar{Name: util.MmsJvmParamEnvVar}
	backupJvmEnvVar := corev1.EnvVar{Name: util.BackupDaemonJvmParamEnvVar}

	// calculate xmx from container's memory limit
	memLimits := template.Spec.Containers[0].Resources.Limits.Memory()
	maxPodMem, err := getPercentOfQuantityAsInt(*memLimits, OpsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xmx from pod mem: %e", err)
	}

	// calculate xms from container's memory request if it is set, otherwise xms=xmx
	memRequests := template.Spec.Containers[0].Resources.Requests.Memory()
	minPodMem, err := getPercentOfQuantityAsInt(*memRequests, OpsManagerPodMemPercentage)
	if err != nil {
		return []corev1.EnvVar{}, fmt.Errorf("error calculating xms from pod mem: %e", err)
	}

	// if only one of mem limits/requests is set, use that value for both xmx & xms
	if minPodMem == 0 {
		minPodMem = maxPodMem
	}
	if maxPodMem == 0 {
		maxPodMem = minPodMem
	}

	memParams := fmt.Sprintf("-Xmx%dm -Xms%dm", maxPodMem, minPodMem)
	mmsJvmEnvVar.Value = buildJvmEnvVar(m.JVMParams, memParams)
	backupJvmEnvVar.Value = buildJvmEnvVar(m.Backup.JVMParams, memParams)

	return []corev1.EnvVar{mmsJvmEnvVar, backupJvmEnvVar}, nil
}

func getPercentOfQuantityAsInt(q resource.Quantity, percent int) (int, error) {
	quantityAsInt, canConvert := q.AsInt64()
	if !canConvert {
		// the container's mem can't be converted to int64, use default of 5G
		podMem, err := resource.ParseQuantity(util.DefaultMemoryOpsManager)
		quantityAsInt, canConvert = podMem.AsInt64()
		if err != nil {
			return 0, err
		}
		if !canConvert {
			return 0, fmt.Errorf("cannot convert %s to int64", podMem.String())
		}
	}
	percentage := float64(percent) / 100.0

	return int(float64(quantityAsInt)*percentage) / OneMB, nil
}

func buildJvmEnvVar(customParams []string, containerMemParams string) string {
	jvmParams := strings.Join(customParams, " ")

	// if both mem limits and mem requests are unset/have value 0, we don't want to override om's default JVM xmx/xms params
	if strings.Contains(containerMemParams, "-Xmx0m") {
		return jvmParams
	}

	if strings.Contains(jvmParams, "Xmx") {
		return jvmParams
	}

	if jvmParams != "" {
		jvmParams += " "
	}

	return jvmParams + containerMemParams
}

// buildService creates the Kube Service. If it should be seen externally it makes it of type NodePort that will assign
// some random port in the range 30000-32767
// Note that itself service has no dedicated IP by default ("clusterIP: None") as all mongo entities should be directly
// addressable.
// This function will update a Service object if passed, or return a new one if passed nil, this is to be able to update
// Services and to not change any attribute they might already have that needs to be maintained.
func buildService(namespacedName types.NamespacedName, owner v1.CustomResourceReadWriter, label string, port int32, mongoServiceDefinition omv1.MongoDBOpsManagerServiceDefinition) corev1.Service {
	labels := map[string]string{
		AppLabelKey:                   label,
		construct.ControllerLabelName: util.OperatorName,
	}
	svcBuilder := service.Builder().
		SetNamespace(namespacedName.Namespace).
		SetName(namespacedName.Name).
		SetPort(port).
		SetOwnerReferences(baseOwnerReference(owner)).
		SetLabels(labels).
		SetSelector(labels).
		SetServiceType(mongoServiceDefinition.Type)

	serviceType := mongoServiceDefinition.Type
	if serviceType == corev1.ServiceTypeNodePort || serviceType == corev1.ServiceTypeLoadBalancer {
		svcBuilder.SetClusterIP("").SetNodePort(mongoServiceDefinition.Port)
	}

	if serviceType == corev1.ServiceTypeClusterIP {
		svcBuilder.SetPublishNotReadyAddresses(true).SetClusterIP("None").SetPortName("mongodb")
	}

	if mongoServiceDefinition.Annotations != nil {
		svcBuilder.SetAnnotations(mongoServiceDefinition.Annotations)
	}

	if mongoServiceDefinition.LoadBalancerIP != "" {
		svcBuilder.SetLoadBalancerIP(mongoServiceDefinition.LoadBalancerIP)
	}

	if mongoServiceDefinition.ExternalTrafficPolicy != "" {
		svcBuilder.SetExternalTrafficPolicy(mongoServiceDefinition.ExternalTrafficPolicy)
	}

	return svcBuilder.Build()
}

func baseOwnerReference(owner v1.CustomResourceReadWriter) []metav1.OwnerReference {
	if owner == nil {
		return []metav1.OwnerReference{}
	}
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   v1.SchemeGroupVersion.Group,
			Version: v1.SchemeGroupVersion.Version,
			Kind:    owner.GetObjectKind().GroupVersionKind().Kind,
		}),
	}
}

// TODO: delete this and move unit tests into construction_test.go
func databaseEnvVars(podVars *PodEnvVars) []corev1.EnvVar {
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
		envVarFromSecret(util.ENV_VAR_AGENT_API_KEY, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey),
		{
			Name:  util.ENV_VAR_LOG_LEVEL,
			Value: string(podVars.LogLevel),
		},
	}

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
		trustedCACertLocation := path.Join(CaCertMountPath, util.CaCertMMS)
		vars = append(vars,
			corev1.EnvVar{
				Name:  util.EnvVarSSLTrustedMMSServerCertificate,
				Value: trustedCACertLocation,
			},
		)
	}

	return vars
}
