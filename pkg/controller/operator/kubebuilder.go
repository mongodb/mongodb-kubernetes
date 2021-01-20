package operator

// This is a collection of functions building different Kubernetes API objects (statefulset, templates etc) from operator
// custom objects
import (
	"path"
	"strconv"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
)

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
func databaseEnvVars(podVars *env.PodEnvVars) []corev1.EnvVar {
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
		env.FromSecret(util.ENV_VAR_AGENT_API_KEY, agentApiKeySecretName(podVars.ProjectID), util.OmAgentApiKey),
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
