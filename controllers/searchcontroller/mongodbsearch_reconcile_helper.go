package searchcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/ghodss/yaml"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/commoncontroller"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

const (
	MongoDBSearchIndexFieldName      = "mdbsearch-for-mongodbresourceref-index"
	unsupportedSearchVersion         = "1.47.0"
	unsupportedSearchVersionErrorFmt = "MongoDBSearch version %s is not supported because of breaking changes. " +
		"The operator will ignore this resource: it will not reconcile or reconfigure the workload. " +
		"Existing deployments will continue to run, but cannot be managed by the operator. " +
		"To regain operator management, you must delete and recreate the MongoDBSearch resource."
)

type OperatorSearchConfig struct {
	SearchRepo    string
	SearchName    string
	SearchVersion string
}

type MongoDBSearchReconcileHelper struct {
	client               kubernetesClient.Client
	mdbSearch            *searchv1.MongoDBSearch
	db                   SearchSourceDBResource
	operatorSearchConfig OperatorSearchConfig
}

func NewMongoDBSearchReconcileHelper(
	client kubernetesClient.Client,
	mdbSearch *searchv1.MongoDBSearch,
	db SearchSourceDBResource,
	operatorSearchConfig OperatorSearchConfig,
) *MongoDBSearchReconcileHelper {
	return &MongoDBSearchReconcileHelper{
		client:               client,
		operatorSearchConfig: operatorSearchConfig,
		mdbSearch:            mdbSearch,
		db:                   db,
	}
}

func (r *MongoDBSearchReconcileHelper) Reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	workflowStatus := r.reconcile(ctx, log)
	if _, err := commoncontroller.UpdateStatus(ctx, r.client, r.mdbSearch, workflowStatus, log); err != nil {
		return workflow.Failed(err)
	}
	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	log = log.With("MongoDBSearch", r.mdbSearch.NamespacedName())
	log.Infof("Reconciling MongoDBSearch")

	if err := r.db.Validate(); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateSearchImageVersion(); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateSingleMongoDBSearchForSearchSource(ctx); err != nil {
		return workflow.Failed(err)
	}

	keyfileStsModification, err := r.ensureSourceKeyfile(ctx, log)
	if apierrors.IsNotFound(err) {
		return workflow.Pending("Waiting for keyfile secret to be created")
	} else if err != nil {
		return workflow.Failed(err)
	}

	if err := r.ensureSearchService(ctx, r.mdbSearch); err != nil {
		return workflow.Failed(err)
	}

	ingressTlsMongotModification, ingressTlsStsModification, err := r.ensureIngressTlsConfig(ctx)
	if err != nil {
		return workflow.Failed(err)
	}

	egressTlsMongotModification, egressTlsStsModification, err := r.ensureEgressTlsConfig(ctx)
	if err != nil {
		return workflow.Failed(err)
	}

	configHash, err := r.ensureMongotConfig(ctx, log, createMongotConfig(r.mdbSearch, r.db), ingressTlsMongotModification, egressTlsMongotModification)
	if err != nil {
		return workflow.Failed(err)
	}

	configHashModification := statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
		map[string]string{
			"mongotConfigHash": configHash,
		},
	))

	if err := r.createOrUpdateStatefulSet(ctx, log, CreateSearchStatefulSetFunc(r.mdbSearch, r.db, r.buildImageString()), configHashModification, keyfileStsModification, ingressTlsStsModification, egressTlsStsModification); err != nil {
		return workflow.Failed(err)
	}

	if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.mdbSearch.Namespace, r.mdbSearch.StatefulSetNamespacedName().Name, r.client); !statefulSetStatus.IsOK() {
		return statefulSetStatus
	}

	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) ensureSourceKeyfile(ctx context.Context, log *zap.SugaredLogger) (statefulset.Modification, error) {
	keyfileSecretName := kube.ObjectKey(r.mdbSearch.GetNamespace(), r.db.KeyfileSecretName())
	keyfileSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, keyfileSecretName, keyfileSecret); err != nil {
		return nil, err
	}

	return statefulset.Apply(
		// make sure mongot pods get restarted if the keyfile changes
		statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
			map[string]string{
				"keyfileHash": hashBytes(keyfileSecret.Data[MongotKeyfileFilename]),
			},
		)),
	), nil
}

func (r *MongoDBSearchReconcileHelper) buildImageString() string {
	imageVersion := r.mdbSearch.Spec.Version
	if imageVersion == "" {
		imageVersion = r.operatorSearchConfig.SearchVersion
	}
	return fmt.Sprintf("%s/%s:%s", r.operatorSearchConfig.SearchRepo, r.operatorSearchConfig.SearchName, imageVersion)
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateStatefulSet(ctx context.Context, log *zap.SugaredLogger, modifications ...statefulset.Modification) error {
	stsName := r.mdbSearch.StatefulSetNamespacedName()
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, sts, func() error {
		statefulset.Apply(modifications...)(sts)
		return nil
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search statefulset %v: %w", stsName, err)
	}

	log.Debugf("Search statefulset %s CreateOrUpdate result: %s", stsName, op)

	return nil
}

func (r *MongoDBSearchReconcileHelper) ensureSearchService(ctx context.Context, search *searchv1.MongoDBSearch) error {
	svcName := search.SearchServiceNamespacedName()
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName.Name, Namespace: svcName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, svc, func() error {
		resourceVersion := svc.ResourceVersion
		*svc = buildSearchHeadlessService(search)
		svc.ResourceVersion = resourceVersion
		return nil
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating search service %v: %w", svcName, err)
	}

	zap.S().Debugf("Updated search service %v: %s", svcName, op)

	return nil
}

func (r *MongoDBSearchReconcileHelper) ensureMongotConfig(ctx context.Context, log *zap.SugaredLogger, modifications ...mongot.Modification) (string, error) {
	mongotConfig := mongot.Config{}
	mongot.Apply(modifications...)(&mongotConfig)
	configData, err := yaml.Marshal(mongotConfig)
	if err != nil {
		return "", err
	}

	cmName := r.mdbSearch.MongotConfigConfigMapNamespacedName()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName.Name, Namespace: cmName.Namespace}, Data: map[string]string{}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, cm, func() error {
		resourceVersion := cm.ResourceVersion

		cm.Data[MongotConfigFilename] = string(configData)

		cm.ResourceVersion = resourceVersion

		return controllerutil.SetOwnerReference(r.mdbSearch, cm, r.client.Scheme())
	})
	if err != nil {
		return "", err
	}

	log.Debugf("Updated mongot config yaml config map: %v (%s) with the following configuration: %s", cmName, op, string(configData))

	return hashBytes(configData), nil
}

func (r *MongoDBSearchReconcileHelper) ensureIngressTlsConfig(ctx context.Context) (mongot.Modification, statefulset.Modification, error) {
	if r.mdbSearch.Spec.Security.TLS == nil {
		mongotModification := func(config *mongot.Config) {
			config.Server.Wireproto.TLS.Mode = mongot.ConfigTLSModeDisabled
		}
		return mongotModification, statefulset.NOOP(), nil
	}

	// TODO: validate that the certificate in the user-provided Secret in .spec.security.tls.certificateKeySecret is issued by the CA in the operator's CA Secret

	certFileName, err := tls.EnsureTLSSecret(ctx, r.client, r.mdbSearch)
	if err != nil {
		return nil, nil, err
	}

	mongotModification := func(config *mongot.Config) {
		certPath := tls.OperatorSecretMountPath + certFileName
		config.Server.Wireproto.TLS.Mode = mongot.ConfigTLSModeTLS
		config.Server.Wireproto.TLS.CertificateKeyFile = ptr.To(certPath)
	}

	tlsSecret := r.mdbSearch.TLSOperatorSecretNamespacedName()
	tlsVolume := statefulset.CreateVolumeFromSecret("tls", tlsSecret.Name)
	tlsVolumeMount := statefulset.CreateVolumeMount("tls", tls.OperatorSecretMountPath, statefulset.WithReadOnly(true))
	statefulsetModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(tlsVolume),
		podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			container.WithVolumeMounts([]corev1.VolumeMount{tlsVolumeMount}),
		)),
	))

	return mongotModification, statefulsetModification, nil
}

func (r *MongoDBSearchReconcileHelper) ensureEgressTlsConfig(ctx context.Context) (mongot.Modification, statefulset.Modification, error) {
	tlsSourceConfig := r.db.TLSConfig()
	if tlsSourceConfig == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	mongotModification := func(config *mongot.Config) {
		config.SyncSource.ReplicaSet.TLS = ptr.To(true)
	}

	_, containerSecurityContext := podtemplatespec.WithDefaultSecurityContextsModifications()
	caVolume := tlsSourceConfig.CAVolume
	trustStoreVolume := statefulset.CreateVolumeFromEmptyDir("cacerts")
	statefulsetModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(caVolume),
		podtemplatespec.WithVolume(trustStoreVolume),
		podtemplatespec.WithInitContainer("init-cacerts", container.Apply(
			container.WithImage(r.buildImageString()),
			containerSecurityContext,
			container.WithVolumeMounts([]corev1.VolumeMount{
				statefulset.CreateVolumeMount(caVolume.Name, tls.CAMountPath, statefulset.WithReadOnly(true)),
				statefulset.CreateVolumeMount(trustStoreVolume.Name, "/java/trust-store", statefulset.WithReadOnly(false)),
			}),
			container.WithCommand([]string{"sh"}),
			container.WithArgs([]string{
				"-c",
				fmt.Sprintf(`
cp /mongot-community/bin/jdk/lib/security/cacerts /java/trust-store/cacerts
/mongot-community/bin/jdk/bin/keytool -keystore /java/trust-store/cacerts -storepass changeit -noprompt -trustcacerts -importcert -alias mongodcert -file %s/%s
							`, tls.CAMountPath, tlsSourceConfig.CAFileName),
			}),
		)),
		podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			container.WithVolumeMounts([]corev1.VolumeMount{
				statefulset.CreateVolumeMount(trustStoreVolume.Name, "/mongot-community/bin/jdk/lib/security/cacerts", statefulset.WithReadOnly(true), statefulset.WithSubPath("cacerts")),
			}),
		)),
	))

	return mongotModification, statefulsetModification, nil
}

func hashBytes(bytes []byte) string {
	hashBytes := sha256.Sum256(bytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:])
}

func buildSearchHeadlessService(search *searchv1.MongoDBSearch) corev1.Service {
	labels := map[string]string{}
	name := search.SearchServiceNamespacedName().Name

	labels["app"] = name

	serviceBuilder := service.Builder().
		SetName(name).
		SetNamespace(search.Namespace).
		SetSelector(labels).
		SetLabels(labels).
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetClusterIP("None").
		SetPublishNotReadyAddresses(true).
		SetOwnerReferences(search.GetOwnerReferences())

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "mongot",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotPort(),
		TargetPort: intstr.FromInt32(search.GetMongotPort()),
	})

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "metrics",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotMetricsPort(),
		TargetPort: intstr.FromInt32(search.GetMongotMetricsPort()),
	})

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "healthcheck",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotHealthCheckPort(),
		TargetPort: intstr.FromInt32(search.GetMongotHealthCheckPort()),
	})

	return serviceBuilder.Build()
}

func createMongotConfig(search *searchv1.MongoDBSearch, db SearchSourceDBResource) mongot.Modification {
	return func(config *mongot.Config) {
		hostAndPorts := db.HostSeeds()

		config.SyncSource = mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				HostAndPort:    hostAndPorts,
				Username:       search.SourceUsername(),
				PasswordFile:   TempSourceUserPasswordPath,
				TLS:            ptr.To(false),
				ReadPreference: ptr.To("secondaryPreferred"),
				AuthSource:     ptr.To("admin"),
			},
		}
		config.Storage = mongot.ConfigStorage{
			DataPath: MongotDataPath,
		}
		config.Server = mongot.ConfigServer{
			Wireproto: &mongot.ConfigWireproto{
				Address: "0.0.0.0:27027",
				Authentication: &mongot.ConfigAuthentication{
					Mode:    "keyfile",
					KeyFile: TempKeyfilePath,
				},
			},
		}
		config.Metrics = mongot.ConfigMetrics{
			Enabled: true,
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotMetricsPort()),
		}
		config.HealthCheck = mongot.ConfigHealthCheck{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotHealthCheckPort()),
		}
		config.Logging = mongot.ConfigLogging{
			Verbosity: string(search.GetLogLevel()),
			LogPath:   nil,
		}
	}
}

func GetMongodConfigParameters(search *searchv1.MongoDBSearch, clusterDomain string) map[string]any {
	searchTLSMode := automationconfig.TLSModeDisabled
	if search.Spec.Security.TLS != nil {
		searchTLSMode = automationconfig.TLSModeRequired
	}
	return map[string]any{
		"setParameter": map[string]any{
			"mongotHost":                                      mongotHostAndPort(search, clusterDomain),
			"searchIndexManagementHostAndPort":                mongotHostAndPort(search, clusterDomain),
			"skipAuthenticationToSearchIndexManagementServer": false,
			"searchTLSMode":                                   string(searchTLSMode),
		},
	}
}

func mongotHostAndPort(search *searchv1.MongoDBSearch, clusterDomain string) string {
	svcName := search.SearchServiceNamespacedName()
	return fmt.Sprintf("%s.%s.svc.%s:%d", svcName.Name, svcName.Namespace, clusterDomain, search.GetMongotPort())
}

func (r *MongoDBSearchReconcileHelper) ValidateSingleMongoDBSearchForSearchSource(ctx context.Context) error {
	if r.mdbSearch.Spec.Source != nil && r.mdbSearch.Spec.Source.ExternalMongoDBSource != nil {
		return nil
	}

	ref := r.mdbSearch.GetMongoDBResourceRef()
	searchList := &searchv1.MongoDBSearchList{}
	if err := r.client.List(ctx, searchList, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(MongoDBSearchIndexFieldName, ref.Namespace+"/"+ref.Name),
	}); err != nil {
		return xerrors.Errorf("Error listing MongoDBSearch resources for search source '%s': %w", ref.Name, err)
	}

	if len(searchList.Items) > 1 {
		resourceNames := make([]string, len(searchList.Items))
		for i, search := range searchList.Items {
			resourceNames[i] = search.Name
		}
		return xerrors.Errorf(
			"Found multiple MongoDBSearch resources for search source '%s': %s", ref.Name,
			strings.Join(resourceNames, ", "),
		)
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) ValidateSearchImageVersion() error {
	version := r.getMongotImage()

	if strings.Contains(version, unsupportedSearchVersion) {
		return xerrors.Errorf(unsupportedSearchVersionErrorFmt, unsupportedSearchVersion)
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) getMongotImage() string {
	version := strings.TrimSpace(r.mdbSearch.Spec.Version)
	if version != "" {
		return version
	}

	if r.operatorSearchConfig.SearchVersion != "" {
		return r.operatorSearchConfig.SearchVersion
	}

	if r.mdbSearch.Spec.StatefulSetConfiguration == nil {
		return ""
	}

	for _, container := range r.mdbSearch.Spec.StatefulSetConfiguration.SpecWrapper.Spec.Template.Spec.Containers {
		if container.Name == MongotContainerName {
			return container.Image
		}
	}

	return ""
}

func SearchCoordinatorRole() mdbv1.MongoDBRole {
	// direct translation of https://github.com/10gen/mongo/blob/6f8d95a513eea8f91ea9f5d895dd8a288dfcf725/src/mongo/db/auth/builtin_roles.yml#L652
	return mdbv1.MongoDBRole{
		Role: "searchCoordinator",
		Db:   "admin",
		Roles: []mdbv1.InheritedRole{
			{
				Role: "clusterMonitor",
				Db:   "admin",
			},
			{
				Role: "directShardOperations",
				Db:   "admin",
			},
			{
				Role: "readAnyDatabase",
				Db:   "admin",
			},
		},
		Privileges: []mdbv1.Privilege{
			{
				Resource: mdbv1.Resource{
					Db: "__mdb_internal_search",
				},
				Actions: []string{
					"changeStream", "collStats", "dbHash", "dbStats", "find",
					"killCursors", "listCollections", "listIndexes", "listSearchIndexes",
					// performRawDataOperations is available only on mongod master
					// "performRawDataOperations",
					"planCacheRead", "cleanupStructuredEncryptionData",
					"compactStructuredEncryptionData", "convertToCapped", "createCollection",
					"createIndex", "createSearchIndexes", "dropCollection", "dropIndex",
					"dropSearchIndex", "insert", "remove", "renameCollectionSameDB",
					"update", "updateSearchIndex",
				},
			},
			// TODO: this causes the error "(BadValue) resource: {cluster: true} conflicts with resource type 'db'"
			// {
			// 	Resource: mdbv1.Resource{
			// 		Cluster: ptr.To(true),
			// 	},
			// 	Actions: []string{"bypassDefaultMaxTimeMS"},
			// },
		},
		AuthenticationRestrictions: nil,
	}
}

// Because the first Search Public Preview support MongoDB Server 8.0.10 we need to polyfill the searchCoordinator role
// TODO: Remove once we drop support for <8.2 in Search
func NeedsSearchCoordinatorRolePolyfill(mongodbVersion string) bool {
	version, err := semver.ParseTolerant(mongodbVersion)
	if err != nil {
		// if we can't determine the version, assume no need to polyfill
		return false
	}

	// 8.0.10+ and 8.1.x need the polyfill, anything older is not supported and execution will never reach here,
	// and anything newer already has the role built-in
	return version.Major == 8 && version.Minor < 2
}
