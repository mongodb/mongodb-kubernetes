package construct

import (
	"context"
	"fmt"
	"net"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	enterprisepem "github.com/10gen/ops-manager-kubernetes/controllers/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1/common"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/lifecycle"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/probes"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/statefulset"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/merge"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
)

const (
	appLabelKey             = "app"
	podAntiAffinityLabelKey = "pod-anti-affinity"
)

// OpsManagerStatefulSetOptions contains all the different values that are variable between
// StatefulSets. Depending on which StatefulSet is being built, a number of these will be pre-set,
// while the remainder will be configurable via configuration functions which modify this type.
type OpsManagerStatefulSetOptions struct {
	OwnerReference               []metav1.OwnerReference
	HTTPSCertSecretName          string
	CertHash                     string
	AppDBTlsCAConfigMapName      string
	AppDBConnectionSecretName    string
	AppDBConnectionStringHash    string
	EnvVars                      []corev1.EnvVar
	InitOpsManagerImage          string
	OpsManagerImage              string
	Name                         string
	Replicas                     int
	ServiceName                  string
	Namespace                    string
	OwnerName                    string
	ServicePort                  int
	QueryableBackupPemSecretName string
	StatefulSetSpecOverride      *appsv1.StatefulSetSpec
	VaultConfig                  vault.VaultConfiguration
	Labels                       map[string]string
	kmip                         *KmipConfiguration
	DebugPort                    int32
	// backup daemon only
	HeadDbPersistenceConfig *common.PersistenceConfig
	Annotations             map[string]string
	LoggingConfiguration    *omv1.Logging
}

type KmipClientConfiguration struct {
	ClientCertificateSecretName            string
	ClientCertificatePasswordSecretName    *string
	ClientCertificatePasswordSecretKeyName *string
}

type KmipConfiguration struct {
	ServerConfiguration  v1.KmipServerConfig
	ClientConfigurations []KmipClientConfiguration
}

func WithConnectionStringHash(hash string) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.AppDBConnectionStringHash = hash
	}
}

func WithVaultConfig(config vault.VaultConfiguration) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.VaultConfig = config
	}
}

func WithInitOpsManagerImage(initOpsManagerImage string) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.InitOpsManagerImage = initOpsManagerImage
	}
}

func WithOpsManagerImage(opsManagerImage string) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.OpsManagerImage = opsManagerImage
	}
}

func WithReplicas(replicas int) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.Replicas = replicas
	}
}

func WithKmipConfig(ctx context.Context, opsManager *omv1.MongoDBOpsManager, client kubernetesClient.Client, log *zap.SugaredLogger) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		if !opsManager.Spec.IsKmipEnabled() {
			return
		}
		opts.kmip = &KmipConfiguration{
			ServerConfiguration:  opsManager.Spec.Backup.Encryption.Kmip.Server,
			ClientConfigurations: make([]KmipClientConfiguration, 0),
		}

		mdbList := &mdbv1.MongoDBList{}
		err := client.List(ctx, mdbList)
		if err != nil {
			log.Warnf("failed to fetch MongoDBList from Kubernetes: %v", err)
		}

		for _, m := range mdbList.Items {
			// Since KMIP integration requires the secret to be mounted into the backup daemon
			// we might not be able to do any encrypted backups across namespaces. Such a backup
			// would require syncing secrets across namespaces.
			// I'm not adding any namespace validation, and we'll let the user handle such synchronization as
			// the backup Daemon will hang in Pending until the secret is provided.
			if m.Spec.Backup != nil && m.Spec.Backup.IsKmipEnabled() {
				c := m.Spec.Backup.Encryption.Kmip.Client
				config := KmipClientConfiguration{
					ClientCertificateSecretName: c.ClientCertificateSecretName(m.GetName()),
				}

				clientCertificatePasswordSecret := &corev1.Secret{}
				err := client.Get(ctx, kube.ObjectKey(m.GetNamespace(), c.ClientCertificatePasswordSecretName(m.GetName())), clientCertificatePasswordSecret)
				if !apiErrors.IsNotFound(err) {
					log.Warnf("failed to fetch the %s Secret from Kubernetes: %v", c.ClientCertificateSecretName(m.GetName()), err)
				} else if err == nil {
					clientCertificateSecretName := c.ClientCertificatePasswordSecretName(m.GetName())
					clientCertificatePasswordKey := c.ClientCertificatePasswordKeyName()
					config.ClientCertificatePasswordSecretKeyName = &clientCertificateSecretName
					config.ClientCertificatePasswordSecretName = &clientCertificatePasswordKey
				}

				opts.kmip.ClientConfigurations = append(opts.kmip.ClientConfigurations, config)
			}
		}
	}
}

func WithStsOverride(stsOverride *appsv1.StatefulSetSpec) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		if stsOverride != nil {
			finalSpec := merge.StatefulSetSpecs(*opts.StatefulSetSpecOverride, *stsOverride)
			opts.StatefulSetSpecOverride = &finalSpec
		}
	}
}

func WithDebugPort(port int32) func(opts *OpsManagerStatefulSetOptions) {
	return func(opts *OpsManagerStatefulSetOptions) {
		opts.DebugPort = port
	}
}

// updateHTTPSCertSecret updates the fields for the OpsManager HTTPS certificate in case the provided secret is of type kubernetes.io/tls.
func (opts *OpsManagerStatefulSetOptions) updateHTTPSCertSecret(ctx context.Context, centralClusterSecretClient secrets.SecretClient, memberCluster multicluster.MemberCluster, ownerReferences []metav1.OwnerReference, log *zap.SugaredLogger) error {
	// Return immediately if no Certificate is provided
	if opts.HTTPSCertSecretName == "" {
		return nil
	}

	var err error
	var secretData map[string][]byte
	var s corev1.Secret
	var opsManagerSecretPath string

	if vault.IsVaultSecretBackend() {
		opsManagerSecretPath = centralClusterSecretClient.VaultClient.OpsManagerSecretPath()
		secretData, err = centralClusterSecretClient.VaultClient.ReadSecretBytes(fmt.Sprintf("%s/%s/%s", opsManagerSecretPath, opts.Namespace, opts.HTTPSCertSecretName))
		if err != nil {
			return err
		}
	} else {
		s, err = centralClusterSecretClient.KubeClient.GetSecret(ctx, kube.ObjectKey(opts.Namespace, opts.HTTPSCertSecretName))
		if err != nil {
			return err
		}

		// SecretTypeTLS is kubernetes.io/tls
		// This is the standard way in K8S to have secrets that hold TLS certs
		// And it is the one generated by cert manager
		// These type of secrets contain tls.crt and tls.key entries
		if s.Type != corev1.SecretTypeTLS {
			return nil
		}
		secretData = s.Data
	}

	data, err := certs.VerifyTLSSecretForStatefulSet(secretData, certs.Options{})
	if err != nil {
		return err
	}

	certHash := enterprisepem.ReadHashFromSecret(ctx, centralClusterSecretClient, opts.Namespace, opts.HTTPSCertSecretName, opsManagerSecretPath, log)

	// The operator concatenates the two fields of the secret into a PEM secret
	err = certs.CreateOrUpdatePEMSecretWithPreviousCert(ctx, memberCluster.SecretClient, kube.ObjectKey(opts.Namespace, opts.HTTPSCertSecretName), certHash, data, ownerReferences, certs.OpsManager)
	if err != nil {
		return err
	}

	opts.HTTPSCertSecretName = fmt.Sprintf("%s%s", opts.HTTPSCertSecretName, certs.OperatorGeneratedCertSuffix)
	opts.CertHash = certHash

	return nil
}

// OpsManagerStatefulSet is the base method for building StatefulSet shared by Ops Manager and Backup Daemon.
// Shouldn't be called by end users directly
func OpsManagerStatefulSet(ctx context.Context, centralClusterSecretClient secrets.SecretClient, opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster, log *zap.SugaredLogger, additionalOpts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
	opts := opsManagerOptions(memberCluster, additionalOpts...)(opsManager)

	opts.Annotations = opsManager.Annotations
	if err := opts.updateHTTPSCertSecret(ctx, centralClusterSecretClient, memberCluster, opsManager.OwnerReferences, log); err != nil {
		return appsv1.StatefulSet{}, err
	}

	secretName := opsManager.Spec.Backup.QueryableBackupSecretRef.Name
	opts.QueryableBackupPemSecretName = secretName
	if secretName != "" {
		// if the secret is specified, we must have a queryable.pem entry.
		_, err := secret.ReadKey(ctx, centralClusterSecretClient, "queryable.pem", kube.ObjectKey(opsManager.Namespace, secretName))
		if err != nil {
			return appsv1.StatefulSet{}, xerrors.Errorf("error reading queryable.pem key from secret %s/%s: %w", opsManager.Namespace, secretName, err)
		}
	}

	opts.LoggingConfiguration = opsManager.Spec.Logging

	omSts := statefulset.New(opsManagerStatefulSetFunc(opts))
	var err error
	if opts.StatefulSetSpecOverride != nil {
		omSts.Spec = merge.StatefulSetSpecs(omSts.Spec, *opts.StatefulSetSpecOverride)
	}

	// the JVM env args must be determined after any potential stateful set override
	// has taken place.
	if err = setJvmArgsEnvVars(opsManager.Spec, util.OpsManagerContainerName, &omSts); err != nil {
		return appsv1.StatefulSet{}, err
	}
	return omSts, nil
}

// getSharedOpsManagerOptions returns the options that are shared between both the OpsManager
// and BackupDaemon StatefulSets
func getSharedOpsManagerOptions(opsManager *omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	return OpsManagerStatefulSetOptions{
		OwnerReference:          kube.BaseOwnerReference(opsManager),
		OwnerName:               opsManager.Name,
		HTTPSCertSecretName:     opsManager.TLSCertificateSecretName(),
		AppDBTlsCAConfigMapName: opsManager.Spec.AppDB.GetCAConfigMapName(),
		EnvVars:                 opsManagerConfigurationToEnvVars(opsManager),
		Namespace:               opsManager.Namespace,
		Labels:                  opsManager.Labels,
	}
}

// opsManagerOptions returns a function which returns the OpsManagerStatefulSetOptions to create the OpsManager StatefulSet
func opsManagerOptions(memberCluster multicluster.MemberCluster, additionalOpts ...func(opts *OpsManagerStatefulSetOptions)) func(opsManager *omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
	return func(opsManager *omv1.MongoDBOpsManager) OpsManagerStatefulSetOptions {
		var stsSpec *appsv1.StatefulSetSpec = nil
		if opsManager.Spec.StatefulSetConfiguration != nil {
			stsSpec = &opsManager.Spec.StatefulSetConfiguration.SpecWrapper.Spec
		}

		_, port := opsManager.GetSchemePort()

		opts := getSharedOpsManagerOptions(opsManager)
		opts.ServicePort = int(port)
		opts.ServiceName = opsManager.SvcName()
		if memberCluster.Legacy {
			opts.Name = opsManager.Name
		} else {
			opts.Name = fmt.Sprintf("%s-%d", opsManager.Name, memberCluster.Index)
		}
		opts.Replicas = memberCluster.Replicas
		opts.StatefulSetSpecOverride = stsSpec
		opts.AppDBConnectionSecretName = opsManager.AppDBMongoConnectionStringSecretName()

		for _, additionalOpt := range additionalOpts {
			additionalOpt(&opts)
		}
		return opts
	}
}

// opsManagerStatefulSetFunc constructs the default Ops Manager StatefulSet modification function.
func opsManagerStatefulSetFunc(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	_, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return statefulset.Apply(
		backupAndOpsManagerSharedConfiguration(opts),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				// 5 minutes for Ops Manager just in case (its internal timeout is 20 seconds anyway)
				podtemplatespec.WithTerminationGracePeriodSeconds(300),
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						configureContainerSecurityContext,
						container.WithCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
						container.WithName(util.OpsManagerContainerName),
						container.WithLivenessProbe(opsManagerLivenessProbe()),
						container.WithStartupProbe(opsManagerStartupProbe()),
						container.WithReadinessProbe(opsManagerReadinessProbe()),
						container.WithLifecycle(buildOpsManagerLifecycle()),
						container.WithEnvs(corev1.EnvVar{Name: "ENABLE_IRP", Value: "true"}),
					),
				),
			)),
	)
}

// backupAndOpsManagerSharedConfiguration returns a function which configures all of the shared
// options between the backup and Ops Manager StatefulSet
func backupAndOpsManagerSharedConfiguration(opts OpsManagerStatefulSetOptions) statefulset.Modification {
	configurePodSpecSecurityContext, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	pullSecretsConfigurationFunc := podtemplatespec.NOOP()
	if pullSecrets, ok := env.Read(util.ImagePullSecrets); ok { // nolint:forbidigo
		pullSecretsConfigurationFunc = podtemplatespec.WithImagePullSecrets(pullSecrets)
	}
	var omVolumeMounts []corev1.VolumeMount

	var omVolumes []corev1.Volume

	if !architectures.IsRunningStaticArchitecture(opts.Annotations) {
		omScriptsVolume := statefulset.CreateVolumeFromEmptyDir("ops-manager-scripts")
		omVolumes = append(omVolumes, omScriptsVolume)
		omScriptsVolumeMount := buildOmScriptsVolumeMount(true)
		omVolumeMounts = append(omVolumeMounts, omScriptsVolumeMount)
	}

	vaultSecrets := vault.OpsManagerSecretsToInject{Config: opts.VaultConfig}
	if vault.IsVaultSecretBackend() {
		vaultSecrets.GenKeyPath = fmt.Sprintf("%s-gen-key", opts.OwnerName)
	} else {
		genKeyVolume := statefulset.CreateVolumeFromSecret("gen-key", fmt.Sprintf("%s-gen-key", opts.OwnerName))
		genKeyVolumeMount := corev1.VolumeMount{
			Name:      genKeyVolume.Name,
			ReadOnly:  true,
			MountPath: util.GenKeyPath,
		}
		omVolumeMounts = append(omVolumeMounts, genKeyVolumeMount)
		omVolumes = append(omVolumes, genKeyVolume)
	}

	if opts.QueryableBackupPemSecretName != "" {
		queryablePemVolume := statefulset.CreateVolumeFromSecret("queryable-pem", opts.QueryableBackupPemSecretName)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      queryablePemVolume.Name,
			ReadOnly:  true,
			MountPath: "/certs/",
		})
		omVolumes = append(omVolumes, queryablePemVolume)
	}

	omHTTPSVolumeFunc := podtemplatespec.NOOP()

	if vault.IsVaultSecretBackend() {
		if opts.HTTPSCertSecretName != "" {
			vaultSecrets.TLSSecretName = opts.HTTPSCertSecretName
			vaultSecrets.TLSHash = opts.CertHash
		}
		vaultSecrets.AppDBConnection = opts.AppDBConnectionSecretName
		vaultSecrets.AppDBConnectionVolume = AppDBConnectionStringPath
	} else if opts.HTTPSCertSecretName != "" {

		omHTTPSCertificateVolume := statefulset.CreateVolumeFromSecret("om-https-certificate", opts.HTTPSCertSecretName)
		omHTTPSVolumeFunc = podtemplatespec.WithVolume(omHTTPSCertificateVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      omHTTPSCertificateVolume.Name,
			MountPath: util.MmsPemKeyFileDirInContainer,
		})

	}

	appDbTLSConfigMapVolumeFunc := podtemplatespec.NOOP()
	if opts.AppDBTlsCAConfigMapName != "" {
		appDbTLSVolume := statefulset.CreateVolumeFromConfigMap("appdb-ca-certificate", opts.AppDBTlsCAConfigMapName)
		appDbTLSConfigMapVolumeFunc = podtemplatespec.WithVolume(appDbTLSVolume)
		omVolumeMounts = append(omVolumeMounts, corev1.VolumeMount{
			Name:      appDbTLSVolume.Name,
			MountPath: util.AppDBMmsCaFileDirInContainer,
		})
	}

	podtemplateAnnotation := podtemplatespec.WithAnnotations(map[string]string{
		"connectionStringHash": opts.AppDBConnectionStringHash,
	})

	if vault.IsVaultSecretBackend() {
		podtemplateAnnotation = podtemplatespec.Apply(
			podtemplateAnnotation,
			podtemplatespec.WithAnnotations(
				vaultSecrets.OpsManagerAnnotations(opts.Namespace),
			),
		)
	}

	if !vault.IsVaultSecretBackend() {
		// configure the AppDB Connection String volume from a secret
		mmsMongoUriVolume, mmsMongoUriVolumeMount := buildMmsMongoUriVolume(opts)
		omVolumeMounts = append(omVolumeMounts, mmsMongoUriVolumeMount)
		omVolumes = append(omVolumes, mmsMongoUriVolume)
	}

	labels := defaultPodLabels(opts.ServiceName, opts.Name)

	// get the labels from the opts and append it to final labels
	stsLabels := defaultPodLabels(opts.ServiceName, opts.Name)
	for k, v := range opts.Labels {
		stsLabels[k] = v
	}

	omVolumes, omVolumeMounts = getNonPersistentOpsManagerVolumeMounts(omVolumes, omVolumeMounts, opts)

	opts.EnvVars = append(opts.EnvVars, kmipEnvVars(opts)...)
	omVolumes, omVolumeMounts = appendKmipVolumes(omVolumes, omVolumeMounts, opts)

	initContainerMod := podtemplatespec.NOOP()

	if !architectures.IsRunningStaticArchitecture(opts.Annotations) {
		initContainerMod = podtemplatespec.WithInitContainerByIndex(0,
			buildOpsManagerAndBackupInitContainer(opts.InitOpsManagerImage),
		)
	}

	return statefulset.Apply(
		statefulset.WithLabels(stsLabels),
		statefulset.WithMatchLabels(labels),
		statefulset.WithName(opts.Name),
		statefulset.WithNamespace(opts.Namespace),
		statefulset.WithOwnerReference(opts.OwnerReference),
		statefulset.WithReplicas(opts.Replicas),
		statefulset.WithServiceName(opts.ServiceName),
		statefulset.WithPodSpecTemplate(
			podtemplatespec.Apply(
				omHTTPSVolumeFunc,
				appDbTLSConfigMapVolumeFunc,
				podtemplateAnnotation,
				podtemplatespec.WithVolumes(omVolumes),
				configurePodSpecSecurityContext,
				podtemplatespec.WithPodLabels(labels),
				pullSecretsConfigurationFunc,
				podtemplatespec.WithServiceAccount(util.OpsManagerServiceAccount),
				podtemplatespec.WithAffinity(opts.Name, podAntiAffinityLabelKey, 100),
				podtemplatespec.WithTopologyKey(util.DefaultAntiAffinityTopologyKey, 0),
				initContainerMod,
				podtemplatespec.WithContainerByIndex(0,
					container.Apply(
						container.WithResourceRequirements(defaultOpsManagerResourceRequirements()),
						container.WithPorts(buildOpsManagerContainerPorts(opts.HTTPSCertSecretName, opts.DebugPort)),
						container.WithImagePullPolicy(corev1.PullPolicy(env.ReadOrPanic(util.OpsManagerPullPolicy))), // nolint:forbidigo
						container.WithImage(opts.OpsManagerImage),
						container.WithEnvs(opts.EnvVars...),
						container.WithEnvs(getOpsManagerHTTPSEnvVars(opts.HTTPSCertSecretName, opts.CertHash)...),
						container.WithCommand([]string{"/opt/scripts/docker-entry-point.sh"}),
						container.WithVolumeMounts(omVolumeMounts),
						configureContainerSecurityContext,
					),
				),
			),
		),
	)
}

func appendKmipVolumes(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, opts OpsManagerStatefulSetOptions) ([]corev1.Volume, []corev1.VolumeMount) {
	if opts.kmip != nil {
		volumes = append(volumes, statefulset.CreateVolumeFromConfigMap(util.KMIPServerCAName, opts.kmip.ServerConfiguration.CA))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.KMIPServerCAName, util.KMIPServerCAHome, statefulset.WithReadOnly(true)))

		for _, cc := range opts.kmip.ClientConfigurations {
			clientSecretName := util.KMIPClientSecretNamePrefix + cc.ClientCertificateSecretName
			clientSecretPath := util.KMIPClientSecretsHome + "/" + cc.ClientCertificateSecretName
			volumes = append(volumes, statefulset.CreateVolumeFromSecret(clientSecretName, cc.ClientCertificateSecretName))
			volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(clientSecretName, clientSecretPath, statefulset.WithReadOnly(true)))
		}
	}
	return volumes, volumeMounts
}

func kmipEnvVars(opts OpsManagerStatefulSetOptions) []corev1.EnvVar {
	if opts.kmip != nil {
		// At this point we are certain, this is correct. We checked it in kmipValidation
		host, port, _ := net.SplitHostPort(opts.kmip.ServerConfiguration.URL)
		return []corev1.EnvVar{
			{
				Name:  util.OmPropertyPrefix + "backup_kmip_server_host",
				Value: host,
			},
			{
				Name:  util.OmPropertyPrefix + "backup_kmip_server_port",
				Value: port,
			},
			{
				Name:  util.OmPropertyPrefix + "backup_kmip_server_ca_file",
				Value: util.KMIPCAFileInContainer,
			},
		}
	}
	return nil
}

// opsManagerReadinessProbe creates the readiness probe.
// Note on 'PeriodSeconds': /monitor/health is a super lightweight method not doing any IO so we can make it more often.
func opsManagerReadinessProbe() probes.Modification {
	return probes.Apply(
		probes.WithInitialDelaySeconds(5),
		probes.WithTimeoutSeconds(5),
		probes.WithPeriodSeconds(5),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(12),
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Scheme: corev1.URISchemeHTTP, Port: intstr.FromInt(8080), Path: "/monitor/health"},
		}),
	)
}

// opsManagerLivenessProbe creates the liveness probe.
func opsManagerLivenessProbe() probes.Modification {
	return probes.Apply(
		probes.WithInitialDelaySeconds(10),
		probes.WithTimeoutSeconds(10),
		probes.WithPeriodSeconds(30),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(24),
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Scheme: corev1.URISchemeHTTP, Port: intstr.FromInt(8080), Path: "/monitor/health"},
		}),
	)
}

// opsManagerStartupProbe creates the startup probe.
func opsManagerStartupProbe() probes.Modification {
	return probes.Apply(
		probes.WithInitialDelaySeconds(1),
		probes.WithTimeoutSeconds(10),
		probes.WithPeriodSeconds(20),
		probes.WithSuccessThreshold(1),
		probes.WithFailureThreshold(30),
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Scheme: corev1.URISchemeHTTP, Port: intstr.FromInt(8080), Path: "/monitor/health"},
		}),
	)
}

// buildOpsManagerAndBackupInitContainer creates the init container which
// copies the entry point script in the OM/Backup container
func buildOpsManagerAndBackupInitContainer(initOpsManagerImage string) container.Modification {
	_, configureContainerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()

	return container.Apply(
		container.WithName(util.InitOpsManagerContainerName),
		container.WithImage(initOpsManagerImage),
		container.WithVolumeMounts([]corev1.VolumeMount{buildOmScriptsVolumeMount(false)}),
		configureContainerSecurityContext,
	)
}

func buildOmScriptsVolumeMount(readOnly bool) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      "ops-manager-scripts",
		MountPath: "/opt/scripts",
		ReadOnly:  readOnly,
	}
}

func buildOpsManagerLifecycle() lifecycle.Modification {
	return lifecycle.WithPrestopCommand([]string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"})
}

func getOpsManagerHTTPSEnvVars(httpsSecretName string, certHash string) []corev1.EnvVar {
	if httpsSecretName != "" {
		path := "server.pem"
		if certHash != "" {
			path = certHash
		}
		// Before creating the podTemplate, we need to add the new PemKeyFile
		// configuration if required.
		return []corev1.EnvVar{{
			Name:  omv1.ConvertNameToEnvVarFormat(util.MmsPEMKeyFile),
			Value: fmt.Sprintf("%s/%s", util.MmsPemKeyFileDirInContainer, path),
		}}
	}
	return []corev1.EnvVar{}
}

func defaultPodLabels(labelKey, antiAffinityKey string) map[string]string {
	return map[string]string{
		appLabelKey:             labelKey,
		ControllerLabelName:     util.OperatorName,
		podAntiAffinityLabelKey: antiAffinityKey,
	}
}

// defaultOpsManagerResourceRequirements returns the default ResourceRequirements
// which are used by OpsManager and the BackupDaemon
func defaultOpsManagerResourceRequirements() corev1.ResourceRequirements {
	defaultMemory, _ := resource.ParseQuantity(util.DefaultMemoryOpsManager)
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: defaultMemory,
		},
		Requests: corev1.ResourceList{},
	}
}

func buildOpsManagerContainerPorts(httpsCertSecretName string, debugPort int32) []corev1.ContainerPort {
	if debugPort > 0 {
		return []corev1.ContainerPort{
			{ContainerPort: getOpsManagerContainerPort(httpsCertSecretName), Name: "default"},
			{ContainerPort: debugPort, Name: "debug"}, // debug
		}
	}

	return []corev1.ContainerPort{{ContainerPort: getOpsManagerContainerPort(httpsCertSecretName)}}
}

func getOpsManagerContainerPort(httpsSecretName string) int32 {
	_, port := omv1.SchemePortFromAnnotation("http")
	if httpsSecretName != "" {
		_, port = omv1.SchemePortFromAnnotation("https")
	}
	return port
}

// opsManagerConfigurationToEnvVars returns a list of corev1.EnvVar which should be passed
// to the container running Ops Manager
func opsManagerConfigurationToEnvVars(m *omv1.MongoDBOpsManager) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for name, value := range m.Spec.Configuration {
		envVars = append(envVars, corev1.EnvVar{
			Name: omv1.ConvertNameToEnvVarFormat(name), Value: value,
		})
	}
	return envVars
}

func hasReleasesVolumeMount(opts OpsManagerStatefulSetOptions) bool {
	if opts.StatefulSetSpecOverride != nil {
		for _, c := range opts.StatefulSetSpecOverride.Template.Spec.Containers {
			for _, vm := range c.VolumeMounts {
				if vm.MountPath == util.OpsManagerPvcMountDownloads {
					return true
				}
			}
		}
	}
	return false
}

func getNonPersistentOpsManagerVolumeMounts(volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, opts OpsManagerStatefulSetOptions) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes = append(volumes, statefulset.CreateVolumeFromEmptyDir(util.OpsManagerPvcNameData))

	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.PvcMountPathTmp, statefulset.WithSubPath(util.PvcNameTmp)))
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.OpsManagerPvcMountPathTmp, statefulset.WithSubPath(util.OpsManagerPvcNameTmp)))
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.OpsManagerPvcMountPathLogs, statefulset.WithSubPath(util.OpsManagerPvcNameLogs)))
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.OpsManagerPvcMountPathEtc, statefulset.WithSubPath(util.OpsManagerPvcNameEtc)))

	if opts.LoggingConfiguration != nil && opts.LoggingConfiguration.LogBackRef != nil {
		volumes = append(volumes, statefulset.CreateVolumeFromConfigMap(util.OpsManagerPvcLogBackNameVolume, opts.LoggingConfiguration.LogBackRef.Name))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcLogBackNameVolume, util.OpsManagerPvcLogbackMountPath, statefulset.WithSubPath(util.OpsManagerPvcLogbackSubPath)))
	}

	if opts.LoggingConfiguration != nil && opts.LoggingConfiguration.LogBackAccessRef != nil {
		volumes = append(volumes, statefulset.CreateVolumeFromConfigMap(util.OpsManagerPvcLogBackAccessNameVolume, opts.LoggingConfiguration.LogBackAccessRef.Name))
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcLogBackAccessNameVolume, util.OpsManagerPvcLogbackAccessMountPath, statefulset.WithSubPath(util.OpsManagerPvcLogbackAccessSubPath)))
	}

	// This content is used by the Ops Manager to download mongodbs. Mount it only if there's no downloads override (like in om_localmode-multiple-pv.yaml for example)
	if !hasReleasesVolumeMount(opts) {
		volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.OpsManagerPvcMountDownloads, statefulset.WithSubPath(util.OpsManagerPvcNameDownloads)))
	}

	// This content is populated by the docker-entry-point.sh. It's being copied from conf-template
	volumeMounts = append(volumeMounts, statefulset.CreateVolumeMount(util.OpsManagerPvcNameData, util.OpsManagerPvcMountPathConf, statefulset.WithSubPath(util.OpsManagerPvcNameConf)))

	return volumes, volumeMounts
}
