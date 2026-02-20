package searchcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
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
	"k8s.io/apimachinery/pkg/types"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/container"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
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

	// embeddingKeyFilePath is the path that is used in mongot config to specify the api keys
	// this where query and index keys would be available.
	embeddingKeyFilePath   = "/etc/mongot/secrets"
	embeddingKeyVolumeName = "auto-embedding-api-keys"

	indexingKeyName = "indexing-key"
	queryKeyName    = "query-key"

	apiKeysTempVolumeName = "api-keys-config"
	// To overcome the strict requirement of api keys having 0400 permission we mount the api keys
	// to a temp location apiKeysTempVolumeMount and then copy it to correct location embeddingKeyFilePath,
	// changing the permission to 0400.
	apiKeysTempVolumeMount = "/tmp/auto-embedding-api-keys"

	// is the minimum search image version that is required to enable the auto embeddings for vector search
	minSearchImageVersionForEmbedding = "0.60.0"

	// autoEmbeddingDetailsAnnKey has the annotation key that would be added to search pod with emebdding API Key secret hash
	autoEmbeddingDetailsAnnKey = "autoEmbeddingDetailsHash"
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
	return workflowStatus
}

func (r *MongoDBSearchReconcileHelper) reconcile(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	log = log.With("MongoDBSearch", r.mdbSearch.NamespacedName())
	log.Infof("Reconciling MongoDBSearch")

	if err := r.db.Validate(); err != nil {
		return workflow.Failed(err)
	}

	version := r.getMongotVersion()

	if err := r.ValidateSearchImageVersion(version); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateSingleMongoDBSearchForSearchSource(ctx); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateMultipleReplicasConfig(); err != nil {
		return workflow.Failed(err)
	}

	if err := r.ValidateAutoEmbeddingConfig(); err != nil {
		return workflow.Failed(err)
	}

	if shardedSource, ok := r.db.(ShardedSearchSourceDBResource); ok {
		return r.reconcileSharded(ctx, log, shardedSource, version)
	}

	// Non-sharded (ReplicaSet) reconciliation
	return r.reconcileNonSharded(ctx, log, version)
}

func (r *MongoDBSearchReconcileHelper) reconcileNonSharded(ctx context.Context, log *zap.SugaredLogger, version string) workflow.Status {
	keyfileStsModification := statefulset.NOOP()
	if r.mdbSearch.IsWireprotoEnabled() {
		var err error
		keyfileStsModification, err = r.ensureSourceKeyfile(ctx, log)
		if apierrors.IsNotFound(err) {
			return workflow.Pending("Waiting for keyfile secret to be created")
		} else if err != nil {
			return workflow.Failed(err)
		}
	}

	if err := r.ensureSearchService(ctx, r.mdbSearch); err != nil {
		return workflow.Failed(err)
	}

	ingressTlsMongotModification, ingressTlsStsModification, err := r.ensureIngressTlsConfig(ctx)
	if err != nil {
		return workflow.Failed(err)
	}

	egressTlsMongotModification, egressTlsStsModification := r.ensureEgressTlsConfig(ctx)

	embeddingConfigMongotModification, embeddingConfigStsModification, err := r.ensureEmbeddingConfig(ctx, log)
	if err != nil {
		return workflow.Failed(err)
	}

	// the egress TLS modification needs to always be applied after the ingress one, because it toggles mTLS based on the mode set by the ingress modification
	configHash, err := r.ensureMongotConfig(ctx, log, createMongotConfig(r.mdbSearch, r.db), ingressTlsMongotModification, egressTlsMongotModification, embeddingConfigMongotModification)
	if err != nil {
		return workflow.Failed(err)
	}

	configHashModification := statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
		map[string]string{
			"mongotConfigHash": configHash,
		},
	))

	image, version := r.searchImageAndVersion()
	mutatedSts, err := r.createOrUpdateStatefulSet(ctx,
		log,
		CreateSearchStatefulSetFunc(r.mdbSearch, r.db, fmt.Sprintf("%s:%s", image, version)),
		configHashModification,
		keyfileStsModification,
		ingressTlsStsModification,
		egressTlsStsModification,
		embeddingConfigStsModification,
	)
	if err != nil {
		return workflow.Failed(err)
	}

	expectedGeneration := mutatedSts.GetGeneration()
	if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.mdbSearch.Namespace, r.mdbSearch.StatefulSetNamespacedName().Name, expectedGeneration, r.client); !statefulSetStatus.IsOK() {
		return statefulSetStatus
	}

	return workflow.OK().WithAdditionalOptions(searchv1.NewMongoDBSearchVersionOption(version))
}

// reconcileSharded deploys one mongot StatefulSet, Service, and ConfigMap per shard.
func (r *MongoDBSearchReconcileHelper) reconcileSharded(ctx context.Context, log *zap.SugaredLogger, shardedSource ShardedSearchSourceDBResource, version string) workflow.Status {
	log.Infof("Reconciling sharded MongoDBSearch with %d shards", shardedSource.GetShardCount())

	keyfileStsModification := statefulset.NOOP()
	if r.mdbSearch.IsWireprotoEnabled() {
		var err error
		keyfileStsModification, err = r.ensureSourceKeyfile(ctx, log)
		if apierrors.IsNotFound(err) {
			return workflow.Pending("Waiting for keyfile secret to be created")
		} else if err != nil {
			return workflow.Failed(err)
		}
	}

	// Validate per-shard TLS secrets exist before proceeding (for per-shard TLS mode)
	shardNames := shardedSource.GetShardNames()
	if status := r.validatePerShardTLSSecrets(ctx, log, shardNames); !status.IsOK() {
		return status
	}

	// Determine TLS mode: shared (all shards use same cert) or per-shard (each shard has its own cert)
	isSharedTLS := r.mdbSearch.IsSharedTLSCertificate()

	// For shared TLS mode, process the shared certificate once before the loop
	var sharedIngressTlsMongotModification mongot.Modification
	var sharedIngressTlsStsModification statefulset.Modification
	if isSharedTLS || r.mdbSearch.Spec.Security.TLS == nil {
		var err error
		sharedIngressTlsMongotModification, sharedIngressTlsStsModification, err = r.ensureIngressTlsConfig(ctx)
		if err != nil {
			return workflow.Failed(err)
		}
	}

	egressTlsMongotModification, egressTlsStsModification := r.ensureEgressTlsConfig(ctx)

	embeddingConfigMongotModification, embeddingConfigStsModification, err := r.ensureEmbeddingConfig(ctx, log)
	if err != nil {
		return workflow.Failed(err)
	}

	image, imageVersion := r.searchImageAndVersion()
	searchImage := fmt.Sprintf("%s:%s", image, imageVersion)

	for shardIdx, shardName := range shardNames {
		shardLog := log.With("shard", shardName, "shardIdx", shardIdx)
		shardLog.Infof("Reconciling mongot for shard %s", shardName)

		if err := r.ensureShardSearchService(ctx, shardName); err != nil {
			return workflow.Failed(err)
		}

		// Determine ingress TLS modifications for this shard
		var ingressTlsMongotModification mongot.Modification
		var ingressTlsStsModification statefulset.Modification
		if isSharedTLS || r.mdbSearch.Spec.Security.TLS == nil {
			// Shared mode or no TLS: use the shared modifications
			ingressTlsMongotModification = sharedIngressTlsMongotModification
			ingressTlsStsModification = sharedIngressTlsStsModification
		} else {
			// Per-shard mode: process this shard's certificate
			var err error
			ingressTlsMongotModification, ingressTlsStsModification, err = r.ensureIngressTlsConfigForShard(ctx, shardName)
			if err != nil {
				return workflow.Failed(err)
			}
		}

		shardMongotConfig := createShardMongotConfig(r.mdbSearch, shardedSource, shardIdx)
		configHash, err := r.ensureShardMongotConfig(ctx, shardLog, shardName, shardMongotConfig, ingressTlsMongotModification, egressTlsMongotModification, embeddingConfigMongotModification)
		if err != nil {
			return workflow.Failed(err)
		}

		configHashModification := statefulset.WithPodSpecTemplate(podtemplatespec.WithAnnotations(
			map[string]string{
				"mongotConfigHash": configHash,
			},
		))

		mutatedSts, err := r.createOrUpdateShardStatefulSet(ctx,
			shardLog,
			shardName,
			CreateShardSearchStatefulSetFunc(r.mdbSearch, shardedSource, shardIdx, searchImage),
			configHashModification,
			keyfileStsModification,
			ingressTlsStsModification,
			egressTlsStsModification,
			embeddingConfigStsModification,
		)
		if err != nil {
			return workflow.Failed(err)
		}

		expectedGeneration := mutatedSts.GetGeneration()
		if statefulSetStatus := statefulset.GetStatefulSetStatus(ctx, r.mdbSearch.Namespace, mutatedSts.Name, expectedGeneration, r.client); !statefulSetStatus.IsOK() {
			return statefulSetStatus
		}
	}

	return workflow.OK().WithAdditionalOptions(searchv1.NewMongoDBSearchVersionOption(version))
}

// This is called only if the wireproto server is enabled, to set up they keyfile necessary for authentication.
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
		CreateKeyfileModificationFunc(r.db.KeyfileSecretName()),
	), nil
}

// validatePerShardTLSSecrets validates that all per-shard TLS source secrets exist.
// Returns workflow.OK() if TLS is not configured, in shared mode, or all secrets exist.
// Returns workflow.Pending if any secret is missing (expected to be created).
// Returns workflow.Failed on other errors.
func (r *MongoDBSearchReconcileHelper) validatePerShardTLSSecrets(ctx context.Context, log *zap.SugaredLogger, shardNames []string) workflow.Status {
	if r.mdbSearch.Spec.Security.TLS == nil {
		return workflow.OK()
	}

	// Shared mode: single secret for all shards, validated by ensureIngressTlsConfig
	if r.mdbSearch.IsSharedTLSCertificate() {
		return workflow.OK()
	}

	// Per-shard mode: validate each shard's source secret exists
	for _, shardName := range shardNames {
		secretNsName := r.mdbSearch.TLSSecretNamespacedNameForShard(shardName)
		tlsSecret := &corev1.Secret{}
		err := r.client.Get(ctx, secretNsName, tlsSecret)
		if apierrors.IsNotFound(err) {
			log.Infof("Waiting for per-shard TLS secret %s to be created", secretNsName)
			return workflow.Pending("Waiting for TLS secret %s for shard %s to be created", secretNsName.Name, shardName)
		} else if err != nil {
			return workflow.Failed(xerrors.Errorf("failed to get TLS secret %s for shard %s: %w", secretNsName.Name, shardName, err))
		}
	}

	return workflow.OK()
}

func (r *MongoDBSearchReconcileHelper) searchImageAndVersion() (string, string) {
	imageVersion := r.mdbSearch.Spec.Version
	if imageVersion == "" {
		imageVersion = r.operatorSearchConfig.SearchVersion
	}
	return fmt.Sprintf("%s/%s", r.operatorSearchConfig.SearchRepo, r.operatorSearchConfig.SearchName), imageVersion
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateStatefulSet(ctx context.Context, log *zap.SugaredLogger, modifications ...statefulset.Modification) (*appsv1.StatefulSet, error) {
	stsName := r.mdbSearch.StatefulSetNamespacedName()
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, sts, func() error {
		statefulset.Apply(modifications...)(sts)
		return nil
	})
	if err != nil {
		return nil, xerrors.Errorf("error creating/updating search statefulset %v: %w", stsName, err)
	}

	log.Debugf("Search statefulset %s CreateOrUpdate result: %s", stsName, op)

	return sts, nil
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

func (r *MongoDBSearchReconcileHelper) ensureShardSearchService(ctx context.Context, shardName string) error {
	svcName := r.mdbSearch.ShardMongotServiceNamespacedName(shardName)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName.Name, Namespace: svcName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, svc, func() error {
		resourceVersion := svc.ResourceVersion
		*svc = buildShardSearchHeadlessService(r.mdbSearch, shardName)
		svc.ResourceVersion = resourceVersion
		return controllerutil.SetOwnerReference(r.mdbSearch, svc, r.client.Scheme())
	})
	if err != nil {
		return xerrors.Errorf("error creating/updating shard search service %v: %w", svcName, err)
	}

	zap.S().Debugf("Updated shard search service %v: %s", svcName, op)

	return nil
}

func (r *MongoDBSearchReconcileHelper) ensureShardMongotConfig(ctx context.Context, log *zap.SugaredLogger, shardName string, modifications ...mongot.Modification) (string, error) {
	mongotConfig := mongot.Config{}
	mongot.Apply(modifications...)(&mongotConfig)
	configData, err := yaml.Marshal(mongotConfig)
	if err != nil {
		return "", err
	}

	cmName := r.mdbSearch.ShardMongotConfigMapNamespacedName(shardName)
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

	log.Debugf("Updated shard mongot config yaml config map: %v (%s) with the following configuration: %s", cmName, op, string(configData))

	return hashBytes(configData), nil
}

func (r *MongoDBSearchReconcileHelper) createOrUpdateShardStatefulSet(ctx context.Context, log *zap.SugaredLogger, shardName string, modifications ...statefulset.Modification) (*appsv1.StatefulSet, error) {
	stsName := r.mdbSearch.ShardMongotStatefulSetNamespacedName(shardName)
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: stsName.Name, Namespace: stsName.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.client, sts, func() error {
		statefulset.Apply(modifications...)(sts)
		return controllerutil.SetOwnerReference(r.mdbSearch, sts, r.client.Scheme())
	})
	if err != nil {
		return nil, xerrors.Errorf("error creating/updating shard search statefulset %v: %w", stsName, err)
	}

	log.Debugf("Shard search statefulset %s CreateOrUpdate result: %s", stsName, op)

	return sts, nil
}

// buildShardSearchHeadlessService builds a headless Service for a specific shard's mongot.
func buildShardSearchHeadlessService(search *searchv1.MongoDBSearch, shardName string) corev1.Service {
	svcName := search.ShardMongotServiceNamespacedName(shardName)
	stsName := search.ShardMongotStatefulSetName(shardName)

	labels := map[string]string{
		"app":   svcName.Name,
		"shard": shardName,
	}

	serviceBuilder := service.Builder().
		SetName(svcName.Name).
		SetNamespace(svcName.Namespace).
		SetLabels(labels).
		SetSelector(map[string]string{"app": stsName}).
		SetClusterIP("None").
		SetPublishNotReadyAddresses(true).
		SetServiceType(corev1.ServiceTypeClusterIP).
		SetOwnerReferences(search.GetOwnerReferences()).
		AddPort(&corev1.ServicePort{
			Name:       "mongot-grpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       search.GetMongotGrpcPort(),
			TargetPort: intstr.FromInt32(search.GetMongotGrpcPort()),
		})

	if prometheus := search.GetPrometheus(); prometheus != nil {
		serviceBuilder.AddPort(&corev1.ServicePort{
			Name:       "prometheus",
			Protocol:   corev1.ProtocolTCP,
			Port:       prometheus.GetPort(),
			TargetPort: intstr.FromInt32(prometheus.GetPort()),
		})
	}

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "healthcheck",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotHealthCheckPort(),
		TargetPort: intstr.FromInt32(search.GetMongotHealthCheckPort()),
	})

	return serviceBuilder.Build()
}

// createShardMongotConfig creates the mongot configuration for a specific shard.
// Each shard's mongot connects to its own shard's mongod hosts.
func createShardMongotConfig(search *searchv1.MongoDBSearch, shardedSource ShardedSearchSourceDBResource, shardIdx int) mongot.Modification {
	return func(config *mongot.Config) {
		hostAndPorts := shardedSource.HostSeedsForShard(shardIdx)

		config.SyncSource = mongot.ConfigSyncSource{
			ReplicaSet: mongot.ConfigReplicaSet{
				HostAndPort:    hostAndPorts,
				Username:       search.SourceUsername(),
				PasswordFile:   TempSourceUserPasswordPath,
				TLS:            ptr.To(false),
				ReadPreference: ptr.To("secondaryPreferred"),
				AuthSource:     ptr.To("admin"),
			},
			// Router configuration for mongos connection in sharded clusters
			Router: &mongot.ConfigRouter{
				HostAndPort:  shardedSource.MongosHostAndPort(),
				Username:     search.SourceUsername(),
				PasswordFile: TempSourceUserPasswordPath,
				TLS:          ptr.To(false),
			},
		}
		config.Storage = mongot.ConfigStorage{
			DataPath: MongotDataPath,
		}
		config.Server = mongot.ConfigServer{
			Grpc: &mongot.ConfigGrpc{
				Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotGrpcPort()),
				TLS: &mongot.ConfigGrpcTLS{
					Mode: mongot.ConfigTLSModeDisabled,
				},
			},
		}

		// Configure prometheus metrics if enabled
		if prometheus := search.GetPrometheus(); prometheus != nil {
			config.Metrics = mongot.ConfigMetrics{
				Enabled: true,
				Address: fmt.Sprintf("0.0.0.0:%d", prometheus.GetPort()),
			}
		}

		// Configure health check endpoint - required for mongot to start
		config.HealthCheck = mongot.ConfigHealthCheck{
			Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotHealthCheckPort()),
		}

		// Configure logging
		config.Logging = mongot.ConfigLogging{
			Verbosity: string(search.GetLogLevel()),
			LogPath:   nil,
		}
	}
}

// EnsureEmbeddingAPIKeySecret makes sure that the scret that is provided in MDBSearch resource
// for embedding model's keys is present and has expected keys.
func ensureEmbeddingAPIKeySecret(ctx context.Context, client secret.Getter, secretObj client.ObjectKey) (string, error) {
	data, err := secret.ReadByteData(ctx, client, secretObj)
	if err != nil {
		return "", err
	}

	if _, ok := data[indexingKeyName]; !ok {
		return "", fmt.Errorf(`Required key "%s" is not present in the Secret %s/%s`, indexingKeyName, secretObj.Namespace, secretObj.Name)
	}
	if _, ok := data[queryKeyName]; !ok {
		return "", fmt.Errorf(`Required key "%s" is not present in the Secret %s/%s`, queryKeyName, secretObj.Namespace, secretObj.Name)
	}

	d, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	return hashBytes(d), nil
}

func validateSearchVesionForEmbedding(version string, log *zap.SugaredLogger) error {
	searchVersion, err := semver.NewVersion(version)
	if err != nil {
		log.Debugf("Failed getting semver of search image version. Version %s doesn't seem to be valid semver.", version)
		return nil
	}
	minAllowedVersion, _ := semver.NewVersion(minSearchImageVersionForEmbedding)

	if a := searchVersion.Compare(minAllowedVersion); a == -1 {
		return xerrors.Errorf("The MongoDB search version %s doesn't support auto embeddings. Please use version %s or newer.", version, minSearchImageVersionForEmbedding)
	}
	return nil
}

// ensureEmbeddingConfig returns the mongot config and stateful set modification function based on the values provided in the search CR, it
// also returns the hash of the secret that has the embedding API keys so that if the keys are changed the search pod is automatically restarted.
func (r *MongoDBSearchReconcileHelper) ensureEmbeddingConfig(ctx context.Context, log *zap.SugaredLogger) (mongot.Modification, statefulset.Modification, error) {
	if r.mdbSearch.Spec.AutoEmbedding == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	// If AutoEmbedding is not nil, it's safe to assume that EmbeddingModelAPIKeySecret would be provided because we have marked it
	// a required field.
	apiKeySecretHash, err := ensureEmbeddingAPIKeySecret(ctx, r.client, client.ObjectKey{
		Name:      r.mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name,
		Namespace: r.mdbSearch.Namespace,
	})
	if err != nil {
		return nil, nil, err
	}

	_, version := r.searchImageAndVersion()
	if err := validateSearchVesionForEmbedding(version, log); err != nil {
		return nil, nil, err
	}

	autoEmbeddingViewWriterTrue := true
	mongotModification := func(config *mongot.Config) {
		config.Embedding = &mongot.EmbeddingConfig{
			IndexingKeyFile: fmt.Sprintf("%s/%s", embeddingKeyFilePath, indexingKeyName),
			QueryKeyFile:    fmt.Sprintf("%s/%s", embeddingKeyFilePath, queryKeyName),
		}

		// Since MCK right now installs search with one replica only it's safe to alway set IsAutoEmbeddingViewWriter to true.
		// Once we start supporting multiple mongot instances, we need to figure this out and then set here.
		config.Embedding.IsAutoEmbeddingViewWriter = &autoEmbeddingViewWriterTrue

		if r.mdbSearch.Spec.AutoEmbedding.ProviderEndpoint != "" {
			config.Embedding.ProviderEndpoint = r.mdbSearch.Spec.AutoEmbedding.ProviderEndpoint
		}
	}
	readOnlyByOwnerPermission := int32(400)
	apiKeyVolume := statefulset.CreateVolumeFromSecret(embeddingKeyVolumeName, r.mdbSearch.Spec.AutoEmbedding.EmbeddingModelAPIKeySecret.Name, statefulset.WithSecretDefaultMode(&readOnlyByOwnerPermission))
	apiKeyVolumeMount := statefulset.CreateVolumeMount(embeddingKeyVolumeName, apiKeysTempVolumeMount, statefulset.WithReadOnly(true))

	emptyDirVolume := statefulset.CreateVolumeFromEmptyDir(apiKeysTempVolumeName)
	emptyDirVolumeMount := statefulset.CreateVolumeMount(apiKeysTempVolumeName, embeddingKeyFilePath)

	stsModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(apiKeyVolume),
		podtemplatespec.WithVolumeMounts(MongotContainerName, apiKeyVolumeMount),
		podtemplatespec.WithVolume(emptyDirVolume),
		podtemplatespec.WithVolumeMounts(MongotContainerName, emptyDirVolumeMount),
		podtemplatespec.WithContainer(MongotContainerName, setupMongotContainerArgsForAPIKeys()),
		podtemplatespec.WithAnnotations(map[string]string{
			autoEmbeddingDetailsAnnKey: apiKeySecretHash,
		}),
	))
	return mongotModification, stsModification, nil
}

func setupMongotContainerArgsForAPIKeys() container.Modification {
	// Since API keys are expected to have 0400 permission, add the arg into the search container to make
	// sure we copy the api keys from temp location (apiKeysTempVolumeMount) to correct location (embeddingKeyFilePath)
	// with correct permissions.
	// Directly setting the permission in the volume doesn't work because volumes are mounted as symlinks and they would have diff permissions,
	// using subpath kind of resolves the probelm but because of fsGroup that we set K8s makes sure that the file is group readable,
	// and that's why the file permissions still don't become 0400 (it's -r--r-----). That's why copying is necessary.
	return prependCommand(sensitiveFilePermissionsForAPIKeys(apiKeysTempVolumeMount, embeddingKeyFilePath, "0400"))
}

func (r *MongoDBSearchReconcileHelper) ensureIngressTlsConfig(ctx context.Context) (mongot.Modification, statefulset.Modification, error) {
	if r.mdbSearch.Spec.Security.TLS == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	certFileName, err := tls.EnsureTLSSecret(ctx, r.client, r.mdbSearch)
	if err != nil {
		return nil, nil, err
	}

	mongotModification := func(config *mongot.Config) {
		certPath := tls.OperatorSecretMountPath + certFileName
		config.Server.Grpc.TLS.Mode = mongot.ConfigTLSModeTLS
		config.Server.Grpc.TLS.CertificateKeyFile = ptr.To(certPath)
		if config.Server.Wireproto != nil {
			config.Server.Wireproto.TLS.Mode = mongot.ConfigTLSModeTLS
			config.Server.Wireproto.TLS.CertificateKeyFile = ptr.To(certPath)
		}
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

// perShardTLSResource wraps MongoDBSearch to provide per-shard TLS secret names.
// It implements the tls.TLSConfigurableResource interface for use with tls.EnsureTLSSecret.
type perShardTLSResource struct {
	*searchv1.MongoDBSearch
	shardName string
}

// TLSSecretNamespacedName returns the per-shard source secret name.
func (p *perShardTLSResource) TLSSecretNamespacedName() types.NamespacedName {
	return p.MongoDBSearch.TLSSecretNamespacedNameForShard(p.shardName)
}

// TLSOperatorSecretNamespacedName returns the per-shard operator-managed secret name.
func (p *perShardTLSResource) TLSOperatorSecretNamespacedName() types.NamespacedName {
	return p.MongoDBSearch.TLSOperatorSecretNamespacedNameForShard(p.shardName)
}

// ensureIngressTlsConfigForShard processes TLS configuration for a specific shard.
// It reads the per-shard source secret and creates the per-shard operator-managed secret.
// Returns mongot and statefulset modifications specific to this shard.
func (r *MongoDBSearchReconcileHelper) ensureIngressTlsConfigForShard(ctx context.Context, shardName string) (mongot.Modification, statefulset.Modification, error) {
	if r.mdbSearch.Spec.Security.TLS == nil {
		return mongot.NOOP(), statefulset.NOOP(), nil
	}

	// Create a per-shard TLS resource adapter
	perShardResource := &perShardTLSResource{
		MongoDBSearch: r.mdbSearch,
		shardName:     shardName,
	}

	certFileName, err := tls.EnsureTLSSecret(ctx, r.client, perShardResource)
	if err != nil {
		return nil, nil, err
	}

	mongotModification := func(config *mongot.Config) {
		certPath := tls.OperatorSecretMountPath + certFileName
		config.Server.Grpc.TLS.Mode = mongot.ConfigTLSModeTLS
		config.Server.Grpc.TLS.CertificateKeyFile = ptr.To(certPath)
		if config.Server.Wireproto != nil {
			config.Server.Wireproto.TLS.Mode = mongot.ConfigTLSModeTLS
			config.Server.Wireproto.TLS.CertificateKeyFile = ptr.To(certPath)
		}
	}

	tlsSecret := perShardResource.TLSOperatorSecretNamespacedName()
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

func (r *MongoDBSearchReconcileHelper) ensureEgressTlsConfig(ctx context.Context) (mongot.Modification, statefulset.Modification) {
	tlsSourceConfig := r.db.TLSConfig()
	if tlsSourceConfig == nil {
		return mongot.NOOP(), statefulset.NOOP()
	}

	mongotModification := func(config *mongot.Config) {
		config.SyncSource.ReplicaSet.TLS = ptr.To(true)
		config.SyncSource.CertificateAuthorityFile = ptr.To(tls.CAMountPath + tlsSourceConfig.CAFileName)

		// For sharded clusters, also enable TLS for the Router (mongos) connection
		if config.SyncSource.Router != nil {
			config.SyncSource.Router.TLS = ptr.To(true)
		}

		// if the gRPC server is configured to accept TLS connections then toggle mTLS as well
		if config.Server.Grpc.TLS.Mode == mongot.ConfigTLSModeTLS {
			config.Server.Grpc.TLS.Mode = mongot.ConfigTLSModeMTLS
			config.Server.Grpc.TLS.CertificateAuthorityFile = config.SyncSource.CertificateAuthorityFile
		}
	}

	caVolume := tlsSourceConfig.CAVolume
	statefulsetModification := statefulset.WithPodSpecTemplate(podtemplatespec.Apply(
		podtemplatespec.WithVolume(caVolume),
		podtemplatespec.WithContainer(MongotContainerName, container.Apply(
			container.WithVolumeMounts([]corev1.VolumeMount{
				statefulset.CreateVolumeMount(caVolume.Name, tls.CAMountPath, statefulset.WithReadOnly(true)),
			}),
		)),
	))

	return mongotModification, statefulsetModification
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
		SetPublishNotReadyAddresses(false).
		SetOwnerReferences(search.GetOwnerReferences())

	if search.IsWireprotoEnabled() {
		serviceBuilder.AddPort(&corev1.ServicePort{
			Name:       "mongot-wireproto",
			Protocol:   corev1.ProtocolTCP,
			Port:       search.GetMongotWireprotoPort(),
			TargetPort: intstr.FromInt32(search.GetMongotWireprotoPort()),
		})
	}

	serviceBuilder.AddPort(&corev1.ServicePort{
		Name:       "mongot-grpc",
		Protocol:   corev1.ProtocolTCP,
		Port:       search.GetMongotGrpcPort(),
		TargetPort: intstr.FromInt32(search.GetMongotGrpcPort()),
	})

	if prometheus := search.GetPrometheus(); prometheus != nil {
		serviceBuilder.AddPort(&corev1.ServicePort{
			Name:       "prometheus",
			Protocol:   corev1.ProtocolTCP,
			Port:       prometheus.GetPort(),
			TargetPort: intstr.FromInt32(prometheus.GetPort()),
		})
	}

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
			Grpc: &mongot.ConfigGrpc{
				Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotGrpcPort()),
				TLS: &mongot.ConfigGrpcTLS{
					Mode: mongot.ConfigTLSModeDisabled,
				},
			},
		}
		if search.IsWireprotoEnabled() {
			config.Server.Wireproto = &mongot.ConfigWireproto{
				Address: fmt.Sprintf("0.0.0.0:%d", search.GetMongotWireprotoPort()),
				Authentication: &mongot.ConfigAuthentication{
					Mode:    "keyfile",
					KeyFile: TempKeyfilePath,
				},
				TLS: &mongot.ConfigWireprotoTLS{
					Mode: mongot.ConfigTLSModeDisabled,
				},
			}
		}

		if prometheus := search.GetPrometheus(); prometheus != nil {
			config.Metrics = mongot.ConfigMetrics{
				Enabled: true,
				Address: fmt.Sprintf("0.0.0.0:%d", prometheus.GetPort()),
			}
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
			"skipAuthenticationToMongot":                      false,
			"searchTLSMode":                                   string(searchTLSMode),
			"useGrpcForSearch":                                !search.IsWireprotoEnabled(),
		},
	}
}

// GetMongodConfigParametersForShard returns the mongod configuration parameters for a specific shard
// in a sharded cluster. When unmanaged LB mode is enabled (spec.lb.mode == Unmanaged with an endpoint
// template), each shard uses the resolved endpoint from the template. Otherwise, the operator-internal
// mongot host is used.
func GetMongodConfigParametersForShard(search *searchv1.MongoDBSearch, shardName string, clusterDomain string) map[string]any {
	searchTLSMode := automationconfig.TLSModeDisabled
	if search.Spec.Security.TLS != nil {
		searchTLSMode = automationconfig.TLSModeRequired
	}

	// Determine the mongot endpoint for this shard
	var mongotEndpoint string
	if search.IsShardedUnmanagedLB() {
		mongotEndpoint = search.GetEndpointForShard(shardName)
	} else {
		// Use the internal shard-local mongot service
		mongotEndpoint = shardMongotHostAndPort(search, shardName, clusterDomain)
	}

	return map[string]any{
		"setParameter": map[string]any{
			"mongotHost":                                      mongotEndpoint,
			"searchIndexManagementHostAndPort":                mongotEndpoint,
			"skipAuthenticationToSearchIndexManagementServer": false,
			"skipAuthenticationToMongot":                      false,
			"searchTLSMode":                                   string(searchTLSMode),
			"useGrpcForSearch":                                !search.IsWireprotoEnabled(),
		},
	}
}

func mongotHostAndPort(search *searchv1.MongoDBSearch, clusterDomain string) string {
	// If unmanaged LB is configured for replica set, use the unmanaged LB endpoint
	if search.IsReplicaSetUnmanagedLB() {
		return search.GetReplicaSetUnmanagedLBEndpoint()
	}

	// Otherwise, use the internal service endpoint
	svcName := search.SearchServiceNamespacedName()
	port := search.GetEffectiveMongotPort()
	return fmt.Sprintf("%s.%s.svc.%s:%d", svcName.Name, svcName.Namespace, clusterDomain, port)
}

// shardMongotHostAndPort returns the internal service endpoint for a shard's mongot deployment
func shardMongotHostAndPort(search *searchv1.MongoDBSearch, shardName string, clusterDomain string) string {
	svcName := search.ShardMongotServiceNamespacedName(shardName)
	port := search.GetEffectiveMongotPort()
	return fmt.Sprintf("%s.%s.svc.%s:%d", svcName.Name, svcName.Namespace, clusterDomain, port)
}

// GetMongosConfigParametersForSharded returns the mongos configuration parameters for a sharded cluster.
// For sharded clusters, mongos needs search parameters to route search queries to mongot.
//
// Required mongos parameters:
// - mongotHost: host:port of the mongot server (or L4/L7 LB fronting mongot)
// - searchIndexManagementHostAndPort: host:port for create/update/drop search indexes (same as mongotHost)
// - useGrpcForSearch: tells mongos to talk to mongot over the MongoDB gRPC protocol (must be true)
//
// For sharded clusters with unmanaged LB, we use the first shard's endpoint as the mongos endpoint.
// This is because mongos needs a single endpoint to route search queries.
func GetMongosConfigParametersForSharded(search *searchv1.MongoDBSearch, shardNames []string, clusterDomain string) map[string]any {
	searchTLSMode := automationconfig.TLSModeDisabled
	if search.Spec.Security.TLS != nil {
		searchTLSMode = automationconfig.TLSModeRequired
	}

	// Determine the mongot endpoint for mongos
	// For sharded clusters, mongos uses the first shard's mongot endpoint
	var mongotEndpoint string
	if len(shardNames) > 0 {
		firstShardName := shardNames[0]
		if search.IsShardedUnmanagedLB() {
			mongotEndpoint = search.GetEndpointForShard(firstShardName)
		} else {
			// Use the internal shard-local mongot service for the first shard
			mongotEndpoint = shardMongotHostAndPort(search, firstShardName, clusterDomain)
		}
	}

	return map[string]any{
		"setParameter": map[string]any{
			"mongotHost":                                      mongotEndpoint,
			"searchIndexManagementHostAndPort":                mongotEndpoint,
			"skipAuthenticationToSearchIndexManagementServer": false,
			"skipAuthenticationToMongot":                      false,
			"searchTLSMode":                                   string(searchTLSMode),
			"useGrpcForSearch":                                true, // Must be true for mongot
		},
	}
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

func (r *MongoDBSearchReconcileHelper) ValidateSearchImageVersion(version string) error {
	if strings.Contains(version, unsupportedSearchVersion) {
		return xerrors.Errorf(unsupportedSearchVersionErrorFmt, unsupportedSearchVersion)
	}

	return nil
}

// ValidateMultipleReplicasConfig validates that when multiple mongot replicas are configured,
// an external load balancer endpoint is also configured to distribute traffic across the replicas.
func (r *MongoDBSearchReconcileHelper) ValidateMultipleReplicasConfig() error {
	if !r.mdbSearch.HasMultipleReplicas() {
		return nil
	}

	// For sharded clusters, check if sharded unmanaged LB is configured
	if _, ok := r.db.(ShardedSearchSourceDBResource); ok {
		if !r.mdbSearch.IsShardedUnmanagedLB() {
			return xerrors.Errorf(
				"multiple mongot replicas (%d) require unmanaged load balancer configuration; "+
					"please configure spec.lb.mode=Unmanaged with spec.lb.endpoint for sharded clusters",
				r.mdbSearch.GetReplicas(),
			)
		}
		return nil
	}

	// For replica sets, check if replica set unmanaged LB is configured
	if !r.mdbSearch.IsReplicaSetUnmanagedLB() {
		return xerrors.Errorf(
			"multiple mongot replicas (%d) require unmanaged load balancer configuration; "+
				"please configure spec.lb.mode=Unmanaged with spec.lb.endpoint",
			r.mdbSearch.GetReplicas(),
		)
	}

	return nil
}

// ValidateAutoEmbeddingConfig validates that auto embeddings are not configured with multiple mongot replicas.
// Auto embeddings require the IsAutoEmbeddingViewWriter flag to be set to true for exactly one mongot instance.
// When multiple replicas are configured, all mongot instances would have this flag set to true, causing conflicts.
func (r *MongoDBSearchReconcileHelper) ValidateAutoEmbeddingConfig() error {
	if r.mdbSearch.Spec.AutoEmbedding == nil {
		return nil
	}

	if r.mdbSearch.HasMultipleReplicas() {
		return xerrors.Errorf(
			"auto embeddings are not supported with multiple mongot replicas (%d); "+
				"please set spec.source.replicas to 1 or remove spec.autoEmbedding",
			r.mdbSearch.GetReplicas(),
		)
	}

	return nil
}

func (r *MongoDBSearchReconcileHelper) getMongotVersion() string {
	version := strings.TrimSpace(r.mdbSearch.Spec.Version)
	if version != "" {
		return version
	}

	version = strings.TrimSpace(r.operatorSearchConfig.SearchVersion)
	if version != "" {
		return version
	}

	if r.mdbSearch.Spec.StatefulSetConfiguration == nil {
		return ""
	}

	for _, container := range r.mdbSearch.Spec.StatefulSetConfiguration.SpecWrapper.Spec.Template.Spec.Containers {
		if container.Name == MongotContainerName {
			return extractImageTag(container.Image)
		}
	}

	return ""
}

func extractImageTag(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}

	if at := strings.Index(image, "@"); at != -1 {
		image = image[:at]
	}

	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		return image[lastColon+1:]
	}

	return ""
}
