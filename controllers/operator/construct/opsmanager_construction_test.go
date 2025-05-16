package construct

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func Test_buildOpsManagerAndBackupInitContainer(t *testing.T) {
	modification := buildOpsManagerAndBackupInitContainer("test-registry:latest")
	containerObj := &corev1.Container{}
	modification(containerObj)
	expectedVolumeMounts := []corev1.VolumeMount{{
		Name:      "ops-manager-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  false,
	}}
	expectedContainer := &corev1.Container{
		Name:         util.InitOpsManagerContainerName,
		Image:        "test-registry:latest",
		VolumeMounts: expectedVolumeMounts,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
		},
	}
	assert.Equal(t, expectedContainer, containerObj)
}

func TestBuildJvmParamsEnvVars_FromCustomContainerResource(t *testing.T) {
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	om.Spec.JVMParams = []string{"-DFakeOptionEnabled"}

	omSts, err := createOpsManagerStatefulset(ctx, om)
	assert.NoError(t, err)
	template := omSts.Spec.Template

	unsetQuantity := *resource.NewQuantity(0, resource.BinarySI)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(268435456, resource.BinarySI)
	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarLimitsOnly, err := buildJvmParamsEnvVars(om.Spec, util.OpsManagerContainerName, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = *resource.NewQuantity(218435456, resource.BinarySI)
	envVarLimitsAndReqs, err := buildJvmParamsEnvVars(om.Spec, util.OpsManagerContainerName, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = unsetQuantity
	envVarReqsOnly, err := buildJvmParamsEnvVars(om.Spec, util.OpsManagerContainerName, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarsNoLimitsOrReqs, err := buildJvmParamsEnvVars(om.Spec, util.OpsManagerContainerName, template)
	assert.NoError(t, err)

	// if only memory requests are configured, xms and xmx should be 90% of mem request
	assert.Equal(t, "-DFakeOptionEnabled -Xmx187m -Xms187m", envVarReqsOnly[0].Value)
	// both are configured, xms should be 90% of mem request, and xmx 90% of mem limit
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms187m", envVarLimitsAndReqs[0].Value)
	// if only memory limits are configured, xms and xmx should be 90% of mem limits
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms230m", envVarLimitsOnly[0].Value)
	// if neither is configured, xms/xmx params should not be added to JVM params, keeping OM defaults
	assert.Equal(t, "-DFakeOptionEnabled", envVarsNoLimitsOrReqs[0].Value)
}

func createOpsManagerStatefulset(ctx context.Context, om *omv1.MongoDBOpsManager, additionalOpts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	omSts, err := OpsManagerStatefulSet(ctx, secretsClient, om, multicluster.GetLegacyCentralMemberCluster(om.Spec.Replicas, 0, client, secretsClient), zap.S(), additionalOpts...)
	return omSts, err
}

func TestBuildJvmParamsEnvVars_FromDefaultPodSpec(t *testing.T) {
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()

	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	omSts, err := OpsManagerStatefulSet(ctx, secretsClient, om, multicluster.GetLegacyCentralMemberCluster(om.Spec.Replicas, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	template := omSts.Spec.Template

	envVar, err := buildJvmParamsEnvVars(om.Spec, util.OpsManagerContainerName, template)
	assert.NoError(t, err)
	// xmx and xms based calculated from  default container memory, requests.mem=limits.mem=5GB
	assert.Equal(t, "CUSTOM_JAVA_MMS_UI_OPTS", envVar[0].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m", envVar[0].Value)

	assert.Equal(t, "CUSTOM_JAVA_DAEMON_OPTS", envVar[1].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m -DDAEMON.DEBUG.PORT=8090", envVar[1].Value)
}

func TestBuildOpsManagerStatefulSet(t *testing.T) {
	ctx := context.Background()
	t.Run("Env Vars Sorted", func(t *testing.T) {
		om := omv1.NewOpsManagerBuilderDefault().
			AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
			AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
			Build()

		sts, err := createOpsManagerStatefulset(ctx, om)

		assert.NoError(t, err)

		// env vars are in sorted order
		expectedVars := []corev1.EnvVar{
			{Name: "ENABLE_IRP", Value: "true"},
			{Name: "OM_PROP_mms_adminEmailAddr", Value: "cloud-manager-support@mongodb.com"},
			{Name: "OM_PROP_mms_centralUrl", Value: "http://om-svc"},
			{Name: "CUSTOM_JAVA_MMS_UI_OPTS", Value: "-Xmx4291m -Xms4291m"},
			{Name: "CUSTOM_JAVA_DAEMON_OPTS", Value: "-Xmx4291m -Xms4291m -DDAEMON.DEBUG.PORT=8090"},
		}
		env := sts.Spec.Template.Spec.Containers[0].Env
		assert.Equal(t, expectedVars, env)
	})
	t.Run("JVM params applied after StatefulSet merge", func(t *testing.T) {
		requirements := corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6G")},
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("400M")},
		}

		statefulSet := statefulset.New(
			statefulset.WithPodSpecTemplate(
				podtemplatespec.Apply(
					podtemplatespec.WithContainer(util.OpsManagerContainerName,
						container.Apply(
							container.WithName(util.OpsManagerContainerName),
							container.WithResourceRequirements(requirements),
						),
					),
				),
			),
		)

		om := omv1.NewOpsManagerBuilderDefault().
			SetStatefulSetSpec(statefulSet.Spec).
			Build()

		sts, err := createOpsManagerStatefulset(ctx, om)
		assert.NoError(t, err)
		expectedVars := []corev1.EnvVar{
			{Name: "ENABLE_IRP", Value: "true"},
			{Name: "CUSTOM_JAVA_MMS_UI_OPTS", Value: "-Xmx5149m -Xms343m"},
			{Name: "CUSTOM_JAVA_DAEMON_OPTS", Value: "-Xmx5149m -Xms343m -DDAEMON.DEBUG.PORT=8090"},
		}
		env := sts.Spec.Template.Spec.Containers[0].Env
		assert.Equal(t, expectedVars, env)
	})
}

func Test_buildOpsManagerStatefulSet(t *testing.T) {
	ctx := context.Background()
	sts, err := createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build())
	assert.NoError(t, err)
	assert.Equal(t, "test-om", sts.Name)
	assert.Equal(t, util.OpsManagerContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.Equal(t, []string{"/opt/scripts/docker-entry-point.sh"},
		sts.Spec.Template.Spec.Containers[0].Command)
}

func Test_buildOpsManagerStatefulSet_Secrets(t *testing.T) {
	ctx := context.Background()
	opsManager := omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build()
	sts, err := createOpsManagerStatefulset(ctx, opsManager)
	assert.NoError(t, err)

	expectedSecretVolumeNames := []string{"test-om-gen-key", opsManager.AppDBMongoConnectionStringSecretName()}
	var actualSecretVolumeNames []string
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.Secret != nil {
			actualSecretVolumeNames = append(actualSecretVolumeNames, v.Secret.SecretName)
		}
	}

	assert.Equal(t, expectedSecretVolumeNames, actualSecretVolumeNames)
}

// TestOpsManagerPodTemplate_MergePodTemplate checks the custom pod template provided by the user.
// It's supposed to override the values produced by the Operator and leave everything else as is
func TestOpsManagerPodTemplate_MergePodTemplate(t *testing.T) {
	ctx := context.Background()
	expectedAnnotations := map[string]string{"customKey": "customVal", "connectionStringHash": ""}
	expectedTolerations := []corev1.Toleration{{Key: "dedicated", Value: "database"}}
	newContainer := corev1.Container{
		Name:  "my-custom-container",
		Image: "my-custom-image",
	}

	podTemplateSpec := podtemplatespec.New(
		podtemplatespec.WithAnnotations(expectedAnnotations),
		podtemplatespec.WithServiceAccount("test-account"),
		podtemplatespec.WithTolerations(expectedTolerations),
		podtemplatespec.WithContainer("my-custom-container",
			container.Apply(
				container.WithName("my-custom-container"),
				container.WithImage("my-custom-image"),
			)),
	)

	om := omv1.NewOpsManagerBuilderDefault().Build()

	omSts, err := createOpsManagerStatefulset(ctx, om)
	assert.NoError(t, err)
	template := omSts.Spec.Template

	originalLabels := template.Labels

	operatorSts := appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: template,
		},
	}

	mergedSpec := merge.StatefulSetSpecs(operatorSts.Spec, appsv1.StatefulSetSpec{
		Template: podTemplateSpec,
	})

	template = mergedSpec.Template
	// Service account gets overriden by custom pod template
	assert.Equal(t, "test-account", template.Spec.ServiceAccountName)
	assert.Equal(t, expectedAnnotations, template.Annotations)
	assert.Equal(t, expectedTolerations, template.Spec.Tolerations)
	assert.Len(t, template.Spec.Containers, 2)
	assert.Equal(t, newContainer, template.Spec.Containers[1])

	// Some validation that the Operator-made config hasn't suffered
	assert.Equal(t, originalLabels, template.Labels)
	assert.NotNil(t, template.Spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), template.Spec.SecurityContext.FSGroup)
	assert.Equal(t, util.OpsManagerContainerName, template.Spec.Containers[0].Name)
}

// TestOpsManagerPodTemplate_PodSpec verifies that StatefulSetSpec is applied correctly to OpsManager/Backup pod template.
func TestOpsManagerPodTemplate_PodSpec(t *testing.T) {
	ctx := context.Background()
	omSts, err := createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)

	resourceLimits := buildSafeResourceList("1.0", "500M")
	resourceRequests := buildSafeResourceList("0.5", "400M")
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	stsSpecOverride := appsv1.StatefulSetSpec{
		Template: podtemplatespec.New(
			podtemplatespec.WithAffinity(omSts.Name, PodAntiAffinityLabelKey, 100),
			podtemplatespec.WithPodAffinity(&podAffinity),
			podtemplatespec.WithNodeAffinity(&nodeAffinity),
			podtemplatespec.WithTopologyKey("rack", 0),
			podtemplatespec.WithContainer(util.OpsManagerContainerName,
				container.Apply(
					container.WithName(util.OpsManagerContainerName),
					container.WithResourceRequirements(corev1.ResourceRequirements{
						Limits:   resourceLimits,
						Requests: resourceRequests,
					}),
				),
			),
		),
	}
	mergedSpec := merge.StatefulSetSpecs(omSts.Spec, stsSpecOverride)
	assert.NoError(t, err)

	spec := mergedSpec.Template.Spec
	assert.Equal(t, defaultNodeAffinity(), *spec.Affinity.NodeAffinity)
	assert.Equal(t, defaultPodAffinity(), *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	// the pod anti affinity term was overridden
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, "rack", term.PodAffinityTerm.TopologyKey)

	req := spec.Containers[0].Resources.Limits
	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "1", (&cpu).String())
	assert.Equal(t, int64(500000000), (&memory).Value())

	req = spec.Containers[0].Resources.Requests
	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "500m", (&cpu).String())
	assert.Equal(t, int64(400000000), (&memory).Value())
}

// TestOpsManagerPodTemplate_SecurityContext verifies that security context is created correctly
// in OpsManager/BackupDaemon podTemplate. It's not built if 'MANAGED_SECURITY_CONTEXT' env var
// is set to 'true'
func TestOpsManagerPodTemplate_SecurityContext(t *testing.T) {
	ctx := context.Background()
	defer mock.InitDefaultEnvVariables()

	omSts, err := createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)

	podSpecTemplate := omSts.Spec.Template
	spec := podSpecTemplate.Spec
	assert.Len(t, spec.InitContainers, 1)
	assert.Equal(t, spec.InitContainers[0].Name, "mongodb-kubernetes-init-ops-manager")
	assert.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	t.Setenv(util.ManagedSecurityContextEnv, "true")

	omSts, err = createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)
	podSpecTemplate = omSts.Spec.Template
	assert.Nil(t, podSpecTemplate.Spec.SecurityContext)
}

func TestOpsManagerPodTemplate_TerminationTimeout(t *testing.T) {
	ctx := context.Background()
	omSts, err := createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)
	podSpecTemplate := omSts.Spec.Template
	assert.Equal(t, int64(300), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

func TestOpsManagerPodTemplate_ImagePullPolicy(t *testing.T) {
	ctx := context.Background()
	defer mock.InitDefaultEnvVariables()

	omSts, err := createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)

	podSpecTemplate := omSts.Spec.Template
	spec := podSpecTemplate.Spec

	assert.Nil(t, spec.ImagePullSecrets)

	t.Setenv(util.ImagePullSecrets, "my-cool-secret")
	omSts, err = createOpsManagerStatefulset(ctx, omv1.NewOpsManagerBuilderDefault().Build())
	assert.NoError(t, err)
	podSpecTemplate = omSts.Spec.Template
	spec = podSpecTemplate.Spec

	assert.NotNil(t, spec.ImagePullSecrets)
	assert.Equal(t, spec.ImagePullSecrets[0].Name, "my-cool-secret")
}

// TestOpsManagerPodTemplate_Container verifies the default OM container built by 'opsManagerPodTemplate' method
func TestOpsManagerPodTemplate_Container(t *testing.T) {
	const opsManagerImage = "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0"

	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build()
	sts, err := createOpsManagerStatefulset(ctx, om, WithOpsManagerImage(opsManagerImage))
	assert.NoError(t, err)
	template := sts.Spec.Template

	assert.Len(t, template.Spec.Containers, 1)
	containerObj := template.Spec.Containers[0]
	assert.Equal(t, util.OpsManagerContainerName, containerObj.Name)
	// TODO change when we stop using versioning
	assert.Equal(t, opsManagerImage, containerObj.Image)
	assert.Equal(t, corev1.PullNever, containerObj.ImagePullPolicy)

	assert.Equal(t, int32(util.OpsManagerDefaultPortHTTP), containerObj.Ports[0].ContainerPort)
	assert.Equal(t, "/monitor/health", containerObj.ReadinessProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), containerObj.ReadinessProbe.HTTPGet.Port.IntVal)
	assert.Equal(t, "/monitor/health", containerObj.LivenessProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), containerObj.LivenessProbe.HTTPGet.Port.IntVal)
	assert.Equal(t, "/monitor/health", containerObj.StartupProbe.HTTPGet.Path)
	assert.Equal(t, int32(8080), containerObj.StartupProbe.HTTPGet.Port.IntVal)

	assert.Equal(t, []string{"/opt/scripts/docker-entry-point.sh"}, containerObj.Command)
	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"},
		containerObj.Lifecycle.PreStop.Exec.Command)
}

func defaultNodeAffinity() corev1.NodeAffinity {
	return corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:    "dc",
						Values: []string{"US-EAST"},
					}},
				},
			},
		},
	}
}

func defaultPodAffinity() corev1.PodAffinity {
	return corev1.PodAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 50,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web-server"}},
				TopologyKey:   "rack",
			},
		}},
	}
}

func defaultPodAntiAffinity() corev1.PodAntiAffinity {
	return corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 77,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web-server"}},
				TopologyKey:   "rack",
			},
		}},
	}
}

// buildSafeResourceList returns a ResourceList but should not be called
// with dynamic values. This function ignores errors in the parsing of
// resource.Quantities and as a result, should only be used in tests with
// pre-set valid values.
func buildSafeResourceList(cpu, memory string) corev1.ResourceList {
	res := corev1.ResourceList{}
	if q := ParseQuantityOrZero(cpu); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := ParseQuantityOrZero(memory); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}
	return res
}
