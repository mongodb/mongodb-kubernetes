package multicluster

import (
	"fmt"
	"path"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// For testing remove this later
func int32Ptr(i int32) *int32                                              { return &i }
func pvModePtr(s corev1.PersistentVolumeMode) *corev1.PersistentVolumeMode { return &s }

func statefulSetName(mdbmName string, clusterNum int) string {
	return fmt.Sprintf("%s-%d", mdbmName, clusterNum)
}

func statefulSetLabels(mdbmName, mdbmNamespace string) map[string]string {
	return map[string]string{
		"controller":   "mongodb-enterprise-operator",
		"mongodbmulti": fmt.Sprintf("%s-%s", mdbmName, mdbmNamespace),
	}
}

func statefulSetAnnotations(mdbmName string, certHash string) map[string]string {
	return map[string]string{
		handler.MongoDBMultiResourceAnnotation: mdbmName,
		certs.CertHashAnnotationkey:            certHash,
	}
}

func statefulSetSelector(mdbmName string) *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"controller":        "mongodb-enterprise-operator",
			"pod-anti-affinity": mdbmName,
		},
	}
}

func PodLabel(mdbmName string) map[string]string {
	return map[string]string{
		"controller":        "mongodb-enterprise-operator",
		"pod-anti-affinity": mdbmName,
	}
}

func mongodbVolumeMount(cmName string, projectConfig mdbv1.ProjectConfig, persistent bool) []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      cmName,
			MountPath: "/opt/scripts/config",
		},
		{
			Name:      construct.AgentAPIKeyVolumeName,
			MountPath: construct.AgentAPIKeySecretPath,
		},
		{
			Name:      "database-scripts",
			MountPath: "/opt/scripts",
			ReadOnly:  true,
		},
	}

	if projectConfig.SSLMMSCAConfigMap != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      construct.CaCertName,
			MountPath: util.SSLMMSCAMountPath,
			ReadOnly:  true,
		})
	}

	if persistent {
		volumeMounts = append(volumeMounts, []corev1.VolumeMount{{
			Name:      "data",
			MountPath: "/data",
			SubPath:   "data",
		},
			{
				Name:      "data",
				MountPath: "/journal",
				SubPath:   "journal",
			},
			{
				Name:      "data",
				MountPath: "/var/log/mongodb-mms-automation",
				SubPath:   "logs",
			},
		}...)
	}
	return volumeMounts
}

func mongodbInitVolumeMount(cmName string) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "database-scripts",
			MountPath: "/opt/scripts",
		},
		{
			Name:      cmName,
			MountPath: "/opt/scripts/config",
		},
	}
}

func mongodbEnv(conn om.Connection, projectConfig mdbv1.ProjectConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:  "AGENT_FLAGS",
			Value: "-logFile,/var/log/mongodb-mms-automation/automation-agent.log,-logLevel,DEBUG,",
		},
		{
			Name:  util.ENV_VAR_BASE_URL,
			Value: conn.BaseURL(),
		},
		{
			Name:  util.ENV_VAR_PROJECT_ID,
			Value: conn.GroupID(),
		},
		{
			Name:  util.ENV_VAR_USER,
			Value: conn.PublicKey(),
		},
		{
			Name:  "MULTI_CLUSTER_MODE",
			Value: "true",
		},
	}
	if projectConfig.SSLMMSCAConfigMap != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  util.EnvVarSSLTrustedMMSServerCertificate,
			Value: path.Join(util.SSLMMSCAMountPath, util.CaCertMMS),
		})
	}
	return envVars
}

func statefulSetVolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	return []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "data",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"storage": resource.MustParse("16G"),
					},
				},
				VolumeMode: pvModePtr(corev1.PersistentVolumeFilesystem),
			},
		},
	}
}

func MultiClusterStatefulSet(mdbm mdbmultiv1.MongoDBMulti, clusterNum int, memberCount int,
	conn om.Connection, projectConfig mdbv1.ProjectConfig, stsOverride appsv1.StatefulSetSpec, certHash string) (appsv1.StatefulSet, error) {

	configurePodSpecSecurityContext, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	// create image for init database container
	version := env.ReadOrDefault(construct.InitDatabaseVersionEnv, "latest")
	initContainerImageURL := construct.ContainerImage(util.InitDatabaseImageUrlEnv, version)

	// create image for database container
	databaseImageVersion := env.ReadOrDefault(construct.DatabaseVersionEnv, "latest")
	databaseImageUrl := construct.ContainerImage(util.AutomationAgentImage, databaseImageVersion)

	pvcVolume := statefulset.NOOP()
	if mdbm.Spec.GetPersistence() {
		pvcVolume = statefulset.Apply(statefulset.WithVolumeClaimTemplates(statefulSetVolumeClaimTemplates()))
	}

	mmsCAVolume := podtemplatespec.NOOP()
	if projectConfig.SSLMMSCAConfigMap != "" {
		mmsCAVolume = podtemplatespec.WithVolume(statefulset.CreateVolumeFromConfigMap(construct.CaCertName, projectConfig.SSLMMSCAConfigMap))
	}

	volumeMounts := make([]corev1.VolumeMount, 0)
	volumes := make([]corev1.Volume, 0)
	volumes, volumeMounts = construct.GetNonPersistentMongoDBVolumeMounts(volumes, volumeMounts)
	volumes, volumeMounts = construct.GetNonPersistentAgentVolumeMounts(volumes, volumeMounts)
	volumeMounts = append(volumeMounts, mongodbVolumeMount(mdbm.GetHostNameOverrideConfigmapName(), projectConfig, mdbm.Spec.GetPersistence())...)

	volumesFunc := func(spec *corev1.PodTemplateSpec) {
		for _, v := range volumes {
			podtemplatespec.WithVolume(v)(spec)
		}
	}

	// create the statefulSet modifications
	stsModifications := statefulset.Apply(
		statefulset.WithName(statefulSetName(mdbm.Name, clusterNum)),
		statefulset.WithNamespace(mdbm.Namespace),
		statefulset.WithLabels(statefulSetLabels(mdbm.Namespace, mdbm.Name)),
		statefulset.WithAnnotations(statefulSetAnnotations(mdbm.Name, certHash)),
		statefulset.WithReplicas(memberCount),
		statefulset.WithSelector(statefulSetSelector(mdbm.Name)),
		statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
			podtemplatespec.WithPodLabels(PodLabel(mdbm.Name)),
			podtemplatespec.WithAffinity(statefulSetName(mdbm.Name, clusterNum), construct.PodAntiAffinityLabelKey, 100),
			podtemplatespec.WithTopologyKey("kubernetes.io/hostname", 0),
			podtemplatespec.WithServiceAccount("mongodb-enterprise-database-pods"),
			configurePodSpecSecurityContext,
			podtemplatespec.WithContainerByIndex(0,
				container.Apply(
					container.WithName(util.DatabaseContainerName),
					container.WithImage(databaseImageUrl),
					container.WithImagePullPolicy(corev1.PullAlways),
					container.WithPorts([]corev1.ContainerPort{{ContainerPort: util.MongoDbDefaultPort, Protocol: "TCP"}}),
					container.WithLivenessProbe(construct.DatabaseLivenessProbe()),
					container.WithReadinessProbe(construct.DatabaseReadinessProbe()),
					container.WithStartupProbe(construct.DatabaseStartupProbe()),
					container.WithCommand([]string{"/opt/scripts/agent-launcher.sh"}),
					container.WithVolumeMounts(volumeMounts),
					container.WithEnvs(mongodbEnv(conn, projectConfig)...),
					container.WithVolumeMounts(mongodbVolumeMount(mdbm.GetHostNameOverrideConfigmapName(), projectConfig, mdbm.Spec.GetPersistence())),
					container.WithEnvs(mongodbEnv(conn, projectConfig)...),
					containerSecurityContext,
				)),
			podtemplatespec.WithVolume(statefulset.CreateVolumeFromEmptyDir("database-scripts")),
			podtemplatespec.WithVolume(statefulset.CreateVolumeFromConfigMap(mdbm.GetHostNameOverrideConfigmapName(), mdbm.GetHostNameOverrideConfigmapName())),
			podtemplatespec.WithVolume(statefulset.CreateVolumeFromSecret(construct.AgentAPIKeyVolumeName, agents.ApiKeySecretName(conn.GroupID()))),
			volumesFunc,
			podtemplatespec.WithTerminationGracePeriodSeconds(600),
			podtemplatespec.WithInitContainerByIndex(0,
				container.WithName(construct.InitDatabaseContainerName),
				container.WithImage(initContainerImageURL),
				container.WithImagePullPolicy(corev1.PullAlways),
				container.WithVolumeMounts(mongodbInitVolumeMount(mdbm.GetHostNameOverrideConfigmapName())),
				containerSecurityContext,
			),
			mmsCAVolume,
		)),
		pvcVolume,
	)

	sts := statefulset.New(stsModifications)

	// Configure STS with TLS, only allow "security.CertificatesSecretsPrefix" in multi-cluster since
	// remaining are deprecated
	if mdbm.GetSecurity().IsTLSEnabled() {
		security := mdbm.GetSecurity()
		tls.ConfigureStatefulSet(&sts, mdbm.Name, security.CertificatesSecretsPrefix, security.TLSConfig.CA)
	}

	currentAgentAuthMode, err := conn.GetAgentAuthMode()
	if err != nil {
		return appsv1.StatefulSet{}, err
	}
	if mdbm.GetSecurity().ShouldUseX509(currentAgentAuthMode) {
		security := mdbm.GetSecurity()
		secretName := fmt.Sprintf("%s-%s-%s-pem", security.CertificatesSecretsPrefix, mdbm.Name, util.AgentSecretName)
		authentication.ConfigureStatefulSetSecret(&sts, secretName)
	}

	stsSpecFinal := merge.StatefulSetSpecs(sts.Spec, stsOverride)
	sts.Spec = stsSpecFinal

	return sts, nil
}
