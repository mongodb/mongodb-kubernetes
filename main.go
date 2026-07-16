package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/zapr"
	"github.com/joho/godotenv"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	golog "log"
	localruntime "runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	runtime_cluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	kubelog "sigs.k8s.io/controller-runtime/pkg/log"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	crWebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	vaiv1 "github.com/mongodb/mongodb-kubernetes/api/voyageai/v1/vai"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mcov1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"                       //nolint:depguard
	mcoController "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers"          //nolint:depguard
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct" //nolint:depguard
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/membercluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/operatorconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/pprof"
	"github.com/mongodb/mongodb-kubernetes/pkg/telemetry"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes/pkg/webhook"
)

const (
	mongoDBCRDPlural             = "mongodb"
	mongoDBUserCRDPlural         = "mongodbusers"
	mongoDBOpsManagerCRDPlural   = "opsmanagers"
	mongoDBMultiClusterCRDPlural = "mongodbmulticluster"
	mongoDBCommunityCRDPlural    = "mongodbcommunity"
	mongoDBSearchCRDPlural       = "mongodbsearch"
	voyageAICRDPlural            = "voyageais"
	clusterMongoDBRoleCRDPlural  = "clustermongodbroles"
)

var (
	log             *zap.SugaredLogger
	operatorEnvOnce sync.Once

	// List of allowed operator environments. The first element of this list is
	// considered the default one.
	operatorEnvironments = []string{util.OperatorEnvironmentDev.String(), util.OperatorEnvironmentLocal.String(), util.OperatorEnvironmentProd.String()}

	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiv1.AddToScheme(scheme))
	utilruntime.Must(operatorv1.AddToScheme(scheme))
	utilruntime.Must(mcov1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(vaiv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	if err := run(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()

	ctx := signals.SetupSignalHandler()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	operator.OmUpdateChannel = make(chan event.GenericEvent)

	klog.InitFlags(nil)
	initializeEnvironment()

	imageUrls := images.LoadImageUrlsFromEnv()
	forceEnterprise := env.ReadBoolOrDefault(architectures.MdbAssumeEnterpriseImage, false)
	initDatabaseNonStaticImageVersion := env.ReadOrDefault(construct.InitDatabaseVersionEnv, "latest")
	databaseNonStaticImageVersion := env.ReadOrDefault(construct.DatabaseVersionEnv, "latest")
	initOpsManagerImageVersion := env.ReadOrDefault(util.InitOpsManagerVersion, "latest")
	backupEnableDelay := time.Duration(env.ReadIntOrPanic(util.BackupStartDelaySecondsEnv)) * time.Second
	// Namespace where the operator is installed
	currentNamespace := env.ReadOrPanic(util.CurrentNamespace)
	webhookSVCSelector := env.ReadOrPanic(util.OperatorNameEnv)

	agentDebug := env.ReadBoolOrDefault(util.EnvVarDebug, false)
	agentDebugImage := env.ReadOrDefault(util.EnvVarDebugImage, "")

	// Get trace and span IDs from environment variables
	traceIDHex := os.Getenv("OTEL_TRACE_ID")
	spanIDHex := os.Getenv("OTEL_PARENT_ID")
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	// Get a config to talk to the apiserver
	cfg := ctrl.GetConfigOrDie()

	operatorConfigName := env.ReadOrDefault(util.OperatorConfigNameEnv, util.DefaultOperatorConfigName)

	log.Debugf("Setting up tracing with ID: %s, Parent ID: %s, Endpoint: %s", traceIDHex, spanIDHex, endpoint)
	ctx, tp, err := telemetry.SetupTracingFromParent(ctx, traceIDHex, spanIDHex, endpoint)
	if err != nil {
		log.Errorf("Failed to setup tracing: %v", err)
	}
	if tp != nil {
		defer shutdownTracerProvider(ctx, tp)
	}

	ctx, operatorSpan := startRootSpan(currentNamespace, spanIDHex, ctx)
	defer operatorSpan.End()

	managerOptions := ctrl.Options{
		Scheme: scheme,
		BaseContext: func() context.Context {
			// Ensures every controller gets the trace and signal-aware context
			return ctx
		},
	}

	namespacesToWatch := operator.GetWatchedNamespace()
	if len(namespacesToWatch) > 1 || namespacesToWatch[0] != "" {
		namespacesForCacheBuilder := namespacesToWatch
		if !stringutil.Contains(namespacesToWatch, currentNamespace) {
			namespacesForCacheBuilder = append(namespacesForCacheBuilder, currentNamespace)
		}
		defaultNamespaces := make(map[string]cache.Config)
		for _, namespace := range namespacesForCacheBuilder {
			defaultNamespaces[namespace] = cache.Config{}
		}
		managerOptions.Cache = cache.Options{
			DefaultNamespaces: defaultNamespaces,
		}
	}

	// Restrict the OperatorConfig informer to the single named instance in the operator's own
	// namespace. Without this, a change to any OperatorConfig in any watched namespace would
	// trigger a restart.
	managerOptions.Cache.ByObject = map[client.Object]cache.ByObject{
		&operatorv1.OperatorConfig{}: {
			Namespaces: map[string]cache.Config{
				currentNamespace: {
					FieldSelector: fields.OneTermEqualSelector("metadata.name", operatorConfigName),
				},
			},
		},
		// Restrict the MemberCluster informer to the operator's own namespace, so only
		// MemberCluster CRs there (the operator's source of truth for cluster membership) can
		// trigger the membercluster.Watcher restart.
		&operatorv1.MemberCluster{}: {
			Namespaces: map[string]cache.Config{
				currentNamespace: {},
			},
		},
	}

	if isInLocalMode() {
		// managerOptions.MetricsBindAddress = "127.0.0.1:8180"
		managerOptions.Metrics = metricsServer.Options{
			BindAddress: "127.0.0.1:8180",
		}
		managerOptions.HealthProbeBindAddress = "127.0.0.1:8181"
	}

	webhookOptions := setupWebhook(ctx, cfg, log, webhookSVCSelector, currentNamespace)
	managerOptions.WebhookServer = crWebhook.NewServer(webhookOptions)

	mgr, err := ctrl.NewManager(cfg, managerOptions)
	if err != nil {
		return err
	}
	operatorCfg, err := operatorconfig.Load(ctx, mgr.GetAPIReader(), currentNamespace, operatorConfigName)
	if err != nil {
		return fmt.Errorf("loading OperatorConfig: %w", err)
	}
	defaultArchitecture := architectures.NonStatic
	if operatorCfg.Spec.DefaultArchitecture == operatorv1.ArchitectureStatic {
		defaultArchitecture = architectures.Static
	}

	// The member-cluster API client timeout is configured via
	// OperatorConfig.spec.multiCluster.memberClusterClientTimeout (seconds).
	// operatorconfig.Load guarantees MultiCluster is non-nil and defaulted.
	memberClusterClientTimeout := operatorCfg.Spec.MultiCluster.MemberClusterClientTimeout

	requiredHealthyStreak := operatorCfg.Spec.MultiCluster.MemberClusterRequiredHealthyStreak

	// Automatic recovery of resources with a broken automation config is configured via
	// OperatorConfig.spec.automaticRecovery. operatorconfig.Load guarantees AutomaticRecovery is
	// non-nil and defaulted.
	automaticRecoveryEnabled := operatorCfg.Spec.AutomaticRecovery.Mode == operatorv1.FeatureModeEnabled
	automaticRecoveryBackoffSeconds := operatorCfg.Spec.AutomaticRecovery.Delay

	propagateProxyEnv := operatorCfg.Spec.Proxy.EnvPropagationPolicy == operatorv1.ProxyEnvPropagationPolicyPropagate

	// Telemetry is configured via OperatorConfig.spec.telemetry. operatorconfig.Load guarantees
	// Telemetry and its nested blocks are non-nil and defaulted, so the pointers below are safe to
	// dereference. Absence of any telemetry configuration implies telemetry is enabled (opt-out model).
	telemetryEnabled := operatorCfg.Spec.Telemetry.Mode == operatorv1.FeatureModeEnabled
	telemetryConfig := telemetry.Config{
		CollectionFrequency: operatorCfg.Spec.Telemetry.Collection.Frequency.Duration,
		KubeTimeout:         operatorCfg.Spec.Telemetry.Collection.KubeTimeout.Duration,
		CollectClusters:     operatorCfg.Spec.Telemetry.Collection.Clusters.Mode == operatorv1.FeatureModeEnabled,
		CollectDeployments:  operatorCfg.Spec.Telemetry.Collection.Deployments.Mode == operatorv1.FeatureModeEnabled,
		CollectOperators:    operatorCfg.Spec.Telemetry.Collection.Operators.Mode == operatorv1.FeatureModeEnabled,
		SendEnabled:         operatorCfg.Spec.Telemetry.Send.Mode == operatorv1.FeatureModeEnabled,
		SendFrequency:       operatorCfg.Spec.Telemetry.Send.Frequency.Duration,
	}

	// The CRDs the operator reconciles are configured via OperatorConfig.spec.watchedResources
	watchedResources := make([]string, len(operatorCfg.Spec.WatchedResources))
	for i, r := range operatorCfg.Spec.WatchedResources {
		watchedResources[i] = string(r)
	}
	enableClusterMongoDBRoles := slices.Contains(watchedResources, clusterMongoDBRoleCRDPlural)

	log.Info("Registering Components.")

	if err := mgr.Add(operatorconfig.NewWatcher(mgr.GetCache(), cancel)); err != nil {
		return err
	}

	// Setup Scheme for all resources
	if err := apiv1.AddToScheme(scheme); err != nil {
		return err
	}

	// memberClusterObjectsMap is a map of clusterName -> clusterObject
	memberClusterObjectsMap := make(map[string]runtime_cluster.Cluster)

	if slices.Contains(watchedResources, mongoDBMultiClusterCRDPlural) {
		// Discover member clusters from MemberCluster CRs + their per-cluster credential Secrets.
		// A direct (uncached) client is used because the manager cache is not started yet.
		directClient, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			return err
		}

		memberClusterClients, usingMemberClusterCRs, err := membercluster.Discover(ctx, directClient, currentNamespace, memberClusterClientTimeout)
		if err != nil {
			return err
		}

		if usingMemberClusterCRs {
			log.Infof("Discovered %d member cluster(s) from MemberCluster CRs", len(memberClusterClients))
			// Watch MemberCluster CRs so the operator restarts and rebuilds this map when
			// cluster membership changes. TODO(m1kola): slice-3: make this reactive (no restart).
			if err := mgr.Add(membercluster.NewWatcher(mgr.GetCache(), cancel)); err != nil {
				return err
			}
		} else {
			// TODO(m1kola): slice-3: legacy fallback — discover member clusters from the
			// <operator>-member-list ConfigMap + the monolithic mounted kubeconfig. Kept so
			// existing multi-cluster installs keep working; removed in slice 6 once all installs
			// use MemberCluster CRs.
			memberClustersNames, err := getMemberClusters(ctx, cfg, currentNamespace)
			if err != nil {
				return err
			}

			log.Infof("Watching Member clusters (legacy discovery): %s", memberClustersNames)

			if len(memberClustersNames) == 0 {
				log.Warnf("The operator did not detect any member clusters")
			}

			memberClusterClients, err = multicluster.CreateMemberClusterClients(memberClustersNames, multicluster.GetKubeConfigPath(), memberClusterClientTimeout)
			if err != nil {
				return err
			}
		}

		// Add the cluster object to the manager corresponding to each member clusters.
		for k, v := range memberClusterClients {
			var cluster runtime_cluster.Cluster

			cluster, err := runtime_cluster.New(v, func(options *runtime_cluster.Options) {
				// Use the operator scheme so cross-cluster owner references
				// can resolve our CRD types (default scheme lacks them).
				options.Scheme = scheme
				if len(namespacesToWatch) > 1 || namespacesToWatch[0] != "" {
					defaultNamespaces := make(map[string]cache.Config)
					for _, namespace := range namespacesToWatch {
						defaultNamespaces[namespace] = cache.Config{}
					}
					options.Cache = cache.Options{
						DefaultNamespaces: defaultNamespaces,
					}
				}
			})
			if err != nil {
				// don't panic here but rather log the error, for example, error might happen when one of the cluster is
				// unreachable, we would still like the operator to continue reconciliation on the other clusters.
				log.Errorf("Failed to initialize client for cluster: %s, err: %s", k, err)
				continue
			}

			log.Infof("Adding cluster %s to cluster map.", k)
			memberClusterObjectsMap[k] = cluster
			if err = mgr.Add(cluster); err != nil {
				return err
			}
		}
	}

	// Setup all Controllers
	if slices.Contains(watchedResources, mongoDBCRDPlural) {
		if err := setupMongoDBCRD(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, automaticRecoveryEnabled, automaticRecoveryBackoffSeconds, memberClusterObjectsMap, backupEnableDelay, operatorCfg.Spec.MaxConcurrentReconciles); err != nil {
			return err
		}
	}
	if slices.Contains(watchedResources, mongoDBOpsManagerCRDPlural) {
		if err := setupMongoDBOpsManagerCRD(ctx, mgr, memberClusterObjectsMap, imageUrls, initDatabaseNonStaticImageVersion, initOpsManagerImageVersion, defaultArchitecture, operatorCfg.Spec.MaxConcurrentReconciles); err != nil {
			return err
		}
	}
	if slices.Contains(watchedResources, mongoDBUserCRDPlural) {
		if err := setupMongoDBUserCRD(ctx, mgr, memberClusterObjectsMap, backupEnableDelay, operatorCfg.Spec.MaxConcurrentReconciles); err != nil {
			return err
		}
	}
	if slices.Contains(watchedResources, mongoDBMultiClusterCRDPlural) {
		if err := setupMongoDBMultiClusterCRD(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, automaticRecoveryEnabled, automaticRecoveryBackoffSeconds, requiredHealthyStreak, memberClusterClientTimeout, memberClusterObjectsMap, operatorCfg.Spec.MaxConcurrentReconciles); err != nil {
			return err
		}
	}
	if slices.Contains(watchedResources, mongoDBSearchCRDPlural) {
		operatorClusterName := env.ReadOrDefault(util.OperatorClusterNameEnv, "")
		if operatorClusterName != "" {
			log.Infof("Per-cluster operator mode enabled for MongoDBSearch: operator cluster identity = %q", operatorClusterName)
		}
		if err := setupMongoDBSearchCRD(ctx, mgr, memberClusterObjectsMap, operatorClusterName, operatorCfg.Spec.MaxConcurrentReconciles, memberClusterClientTimeout, requiredHealthyStreak); err != nil {
			return err
		}
	}
	if slices.Contains(watchedResources, voyageAICRDPlural) {
		if err := setupVoyageAICRD(ctx, mgr, operatorCfg.Spec.MaxConcurrentReconciles); err != nil {
			return err
		}
	}

	for _, r := range watchedResources {
		log.Infof("Registered CRD: %s", r)
	}

	if slices.Contains(watchedResources, mongoDBCommunityCRDPlural) {
		if err := setupCommunityController(
			ctx,
			mgr,
			env.ReadOrDefault(mcoConstruct.MongodbCommunityRepoUrlEnv, "quay.io/mongodb"),
			// when running MCO resource -> mongodb-community-server
			// when running appdb -> mongodb-enterprise-server
			env.ReadOrPanic(mcoConstruct.MongodbCommunityImageEnv),
			env.ReadOrDefault(mcoConstruct.MongoDBCommunityImageTypeEnv, mcoConstruct.DefaultImageType),
			env.ReadOrPanic(util.MongodbCommunityAgentImageEnv),
			env.ReadOrPanic(mcoConstruct.VersionUpgradeHookImageEnv),
			env.ReadOrPanic(mcoConstruct.ReadinessProbeImageEnv),
		); err != nil {
			return err
		}
	}

	if telemetryEnabled {
		log.Info("Running telemetry component!")
		installerMethod := env.ReadOrDefault(telemetry.InstallerEnvVar, "")
		telemetryRunnable, err := telemetry.NewLeaderRunnable(mgr, memberClusterObjectsMap, currentNamespace, imageUrls[util.MongodbImageEnv], imageUrls[util.NonStaticDatabaseEnterpriseImage], installerMethod, getOperatorEnv(), defaultArchitecture, telemetryConfig)
		if err != nil {
			log.Errorf("Unable to enable telemetry; err: %s", err)
		}
		if err := mgr.Add(telemetryRunnable); err != nil {
			log.Errorf("Unable to enable telemetry; err: %s", err)
		}
	} else {
		log.Info("Not running telemetry component!")
	}

	pprofEnabledString := env.ReadOrDefault(util.OperatorPprofEnabledEnv, "")
	if pprofEnabled, err := pprof.IsPprofEnabled(pprofEnabledString, getOperatorEnv()); err != nil {
		log.Errorf("Unable to check if pprof is enabled: %s", err)
	} else if pprofEnabled {
		port := env.ReadIntOrDefault(util.OperatorPprofPortEnv, util.OperatorPprofDefaultPort)
		if err := mgr.Add(pprof.NewRunnable(port, log)); err != nil {
			log.Errorf("Unable to start pprof server: %s", err)
		}
	}

	log.Info("Starting the Cmd.")

	return mgr.Start(ctx)
}

func startRootSpan(currentNamespace string, spanIDHex string, traceCtx context.Context) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithAttributes(
			attribute.String("component", "operator"),
			attribute.String("namespace", currentNamespace),
			attribute.String("service.name", "mongodb-kubernetes-operator"),
			// let's ensure that the root span follows the given parent span
			attribute.String("trace.parent_id", spanIDHex),
		),
	}

	ctx, operatorSpan := telemetry.TRACER.Start(traceCtx, "MONGODB_OPERATOR_ROOT", opts...)
	log.Debugf("Started root operator span with ID: %s in trace %s", operatorSpan.SpanContext().SpanID().String(), operatorSpan.SpanContext().TraceID().String())
	return ctx, operatorSpan
}

func shutdownTracerProvider(signalCtx context.Context, tp *sdktrace.TracerProvider) {
	shutdownCtx, cancel := context.WithTimeout(signalCtx, 5*time.Second)
	defer cancel()

	if err := tp.Shutdown(shutdownCtx); err != nil {
		log.Errorf("Error shutting down tracer provider: %v", err)
	} else {
		log.Debug("Tracer provider successfully shut down")
	}
}

func setupMongoDBCRD(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string, defaultArchitecture architectures.DefaultArchitecture, propagateProxyEnv bool, automaticRecoveryEnabled bool, automaticRecoveryBackoffSeconds int, memberClusterObjectsMap map[string]runtime_cluster.Cluster, backupEnableDelay time.Duration, maxConcurrentReconciles int) error {
	if err := operator.AddStandaloneController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, maxConcurrentReconciles); err != nil {
		return err
	}
	if err := operator.AddReplicaSetController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, automaticRecoveryEnabled, automaticRecoveryBackoffSeconds, maxConcurrentReconciles); err != nil {
		return err
	}
	if err := operator.AddShardedClusterController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, automaticRecoveryEnabled, automaticRecoveryBackoffSeconds, memberClusterObjectsMap, backupEnableDelay, maxConcurrentReconciles); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&mdbv1.MongoDB{}).
		WithValidator(&mdbv1.MongoDBValidator{}).
		Complete()
}

func setupMongoDBOpsManagerCRD(ctx context.Context, mgr manager.Manager, memberClusterObjectsMap map[string]runtime_cluster.Cluster, imageUrls images.ImageUrls, initDatabaseVersion, initOpsManagerImageVersion string, defaultArchitecture architectures.DefaultArchitecture, maxConcurrentReconciles int) error {
	if err := operator.AddOpsManagerController(ctx, mgr, memberClusterObjectsMap, imageUrls, initDatabaseVersion, initOpsManagerImageVersion, defaultArchitecture, maxConcurrentReconciles); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&omv1.MongoDBOpsManager{}).
		WithValidator(&omv1.MongoDBOpsManagerValidator{}).
		Complete()
}

func setupMongoDBUserCRD(ctx context.Context, mgr manager.Manager, memberClusterObjectsMap map[string]runtime_cluster.Cluster, backupEnableDelay time.Duration, maxConcurrentReconciles int) error {
	return operator.AddMongoDBUserController(ctx, mgr, memberClusterObjectsMap, backupEnableDelay, maxConcurrentReconciles)
}

func setupMongoDBMultiClusterCRD(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise, enableClusterMongoDBRoles, agentDebug bool, agentDebugImage string, defaultArchitecture architectures.DefaultArchitecture, propagateProxyEnv bool, automaticRecoveryEnabled bool, automaticRecoveryBackoffSeconds int, requiredHealthyStreak int, memberClusterClientTimeout int, memberClusterObjectsMap map[string]runtime_cluster.Cluster, maxConcurrentReconciles int) error {
	if err := operator.AddMultiReplicaSetController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, agentDebug, agentDebugImage, defaultArchitecture, propagateProxyEnv, automaticRecoveryEnabled, automaticRecoveryBackoffSeconds, requiredHealthyStreak, memberClusterClientTimeout, memberClusterObjectsMap, maxConcurrentReconciles); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&mdbmultiv1.MongoDBMultiCluster{}).
		WithValidator(&mdbmultiv1.MongoDBMultiClusterValidator{}).
		Complete()
}

func setupVoyageAICRD(ctx context.Context, mgr manager.Manager, maxConcurrentReconciles int) error {
	imageRepository := env.ReadOrDefault(util.VoyageAIRepoURLEnv, "quay.io/mongodb/voyageai")
	return operator.AddVoyageAIController(ctx, mgr, imageRepository, maxConcurrentReconciles)
}

func setupMongoDBSearchCRD(
	ctx context.Context,
	mgr manager.Manager,
	memberClusterObjectsMap map[string]runtime_cluster.Cluster,
	operatorClusterName string,
	maxConcurrentReconciles int,
	memberClusterClientTimeout int,
	requiredHealthyStreak int,
) error {
	if err := operator.AddMongoDBSearchController(ctx, mgr, searchcontroller.OperatorSearchConfig{
		SearchRepo:    env.ReadOrPanic(util.SearchRepoURLEnv),
		SearchName:    env.ReadOrPanic(util.SearchNameEnv),
		SearchVersion: env.ReadOrPanic(util.SearchVersionEnv),
	}, memberClusterObjectsMap, operatorClusterName, maxConcurrentReconciles, memberClusterClientTimeout, requiredHealthyStreak); err != nil {
		return err
	}

	// We cannot use ReadOrPanic here because this variable is only needed when Search is used with a managed load
	// balancer
	envoyImage := env.ReadOrDefault(util.EnvoyImageEnv, "")
	if err := operator.AddMongoDBSearchEnvoyController(ctx, mgr, envoyImage, memberClusterObjectsMap, operatorClusterName, maxConcurrentReconciles); err != nil {
		return err
	}

	// Metrics forwarder controller — image is again enforced in controller
	metricsForwarderImage := env.ReadOrDefault(util.MetricsForwarderImageEnv, "")
	if err := operator.AddMongoDBSearchMetricsForwarderController(ctx, mgr, metricsForwarderImage, memberClusterObjectsMap, operatorClusterName, maxConcurrentReconciles); err != nil {
		return err
	}

	return nil
}

func setupCommunityController(
	_ context.Context,
	mgr manager.Manager,
	mongodbRepoURL string,
	mongodbImage string,
	mongodbImageType string,
	agentImage string,
	versionUpgradeHookImage string,
	readinessProbeImage string,
) error {
	return mcoController.NewReconciler(
		mgr,
		mongodbRepoURL, //
		mongodbImage,   // defaults to enterprise in appdb, here should be community
		mongodbImageType,
		agentImage,
		versionUpgradeHookImage,
		readinessProbeImage,
	).SetupWithManager(mgr)
}

// getMemberClusters retrieves the member clusters from the configmap util.MemberListConfigMapName
func getMemberClusters(ctx context.Context, cfg *rest.Config, currentNamespace string) ([]string, error) {
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		panic(err)
	}

	m := corev1.ConfigMap{}
	err = c.Get(ctx, types.NamespacedName{Name: util.MemberListConfigMapName, Namespace: currentNamespace}, &m)
	if apierrors.IsNotFound(err) {
		// No multi-cluster configuration present: run as single-cluster with no member clusters.
		// The member-list ConfigMap is absent on single-cluster installs (e.g. when
		// mongodbmulticluster is watched by default). Callers handle an empty member list.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var members []string
	for member := range m.Data {
		members = append(members, member)
	}

	return members, nil
}

func isInLocalMode() bool {
	return operatorEnvironments[1] == env.ReadOrDefault(util.OmOperatorEnv, util.OperatorEnvironmentProd.String())
}

// setupWebhook sets up the validation webhook for MongoDB resources in order
// to give people early warning when their MongoDB resources are wrong.
func setupWebhook(ctx context.Context, cfg *rest.Config, log *zap.SugaredLogger, svcSelector string, currentNamespace string) crWebhook.Options {
	// set webhook port — 1993 is chosen as Ben's birthday
	webhookPort := env.ReadIntOrDefault(util.MdbWebhookPortEnv, 1993)

	// this is the default directory on Linux but setting it explicitly helps
	// with cross-platform compatibility, specifically local development on MacOS
	certDir := "/tmp/k8s-webhook-server/serving-certs/"
	var webhookHost string
	if isInLocalMode() {
		webhookHost = "127.0.0.1"
	}

	// create a kubernetes client that the webhook server can use. We can't reuse
	// the one from the manager as it is not initialised yet.
	webhookClient, err := client.New(cfg, client.Options{})
	if err != nil {
		panic(err)
	}

	// webhookServiceLocation is the name and namespace of the webhook service
	// that will be created.
	webhookServiceLocation := types.NamespacedName{
		Name:      "operator-webhook",
		Namespace: currentNamespace,
	}

	if err := webhook.Setup(ctx, webhookClient, webhookServiceLocation, certDir, webhookPort, svcSelector, log); err != nil {
		log.Errorf("could not set up webhook: %v", err)
	}

	return crWebhook.Options{
		Port:    webhookPort,
		Host:    webhookHost,
		CertDir: certDir,
	}
}

func initializeEnvironment() {
	omOperatorEnv := getOperatorEnv()

	initEnvVariables()

	log.Infof("Operator environment: %s", omOperatorEnv)

	if omOperatorEnv == util.OperatorEnvironmentDev || omOperatorEnv == util.OperatorEnvironmentLocal {
		log.Infof("Operator build info:\n%s", getBuildSettingsString())
	}

	log.Infof("Operator version: %s", util.OperatorVersion)
	log.Infof("Go Version: %s", localruntime.Version())
	log.Infof("Go OS/Arch: %s/%s", localruntime.GOOS, localruntime.GOARCH)

	printEnvVariables()
}

// loadEnvFromLocalFileForDevelopment loads env vars from .generated/context.operator.env if not running in "prod" env
func loadEnvFromLocalFileForDevelopment() {
	if getOperatorEnv() == util.OperatorEnvironmentProd {
		return
	}

	envFile := ".generated/context.operator.env"
	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			log.Warnf("Failed to load environment variables from file %s: %v", envFile, err)
		} else {
			log.Infof("Loaded environment variables from file %s", envFile)
		}
	}
}

func printEnvVariables() {
	printableEnvPrefixes := []string{
		"BACKUP_WAIT_",
		"POD_WAIT_",
		"OPERATOR_ENV",
		"WATCH_NAMESPACE",
		"NAMESPACE",
		"MANAGED_SECURITY_CONTEXT",
		"IMAGE_PULL_SECRETS",
		"MONGODB_ENTERPRISE_",
		"OPS_MANAGER_",
		"KUBERNETES_",
		"AGENT_IMAGE",
		"MONGODB_",
		"INIT_",
		"MDB_",
		"READINESS_PROBE_IMAGE",
		"VERSION_UPGRADE_HOOK_IMAGE",
		"OTEL_TRACE_ID",
		"OTEL_PARENT_ID",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
	}

	// Only env variables with one of these prefixes will be printed
	env.PrintWithPrefix(printableEnvPrefixes)
}

func getOperatorEnv() util.OperatorEnvironment {
	operatorFromEnv := os.Getenv(util.OmOperatorEnv)
	operatorEnv := util.OperatorEnvironment(operatorFromEnv)
	if !validateOperatorEnv(operatorEnv) {
		operatorEnvOnce.Do(func() {
			golog.Printf("Configured environment %s, not recognized. Must be one of %v", operatorEnv, operatorEnvironments)
			golog.Printf("Using default environment, %s, instead", util.OperatorEnvironmentProd)
		})
		operatorEnv = util.OperatorEnvironmentProd
	}
	return operatorEnv
}

// quoteKey reports whether key is required to be quoted. Taken from: 1.22.0 mod.go
func quoteKey(key string) bool {
	return len(key) == 0 || strings.ContainsAny(key, "= \t\r\n\"`")
}

// quoteValue reports whether value is required to be quoted. Taken from: 1.22.0 mod.go
func quoteValue(value string) bool {
	return strings.ContainsAny(value, " \t\r\n\"`")
}

func getBuildSettingsString() string {
	var buf strings.Builder
	info, _ := debug.ReadBuildInfo()
	for _, s := range info.Settings {
		key := s.Key
		if quoteKey(key) {
			key = strconv.Quote(key)
		}
		value := s.Value
		if quoteValue(value) {
			value = strconv.Quote(value)
		}
		buf.WriteString(fmt.Sprintf("build\t%s=%s\n", key, value))
	}
	return buf.String()
}

// initEnvVariables is the central place in application to initialize default configuration for the application (using
// env variables). Having the central place to manage defaults increases manageability and transparency of the application
// Method initializes variables only in case they are not specified already.
func initEnvVariables() {
	loadEnvFromLocalFileForDevelopment()

	env.EnsureVar(util.BackupDisableWaitSecondsEnv, util.DefaultBackupDisableWaitSeconds)
	env.EnsureVar(util.BackupDisableWaitRetriesEnv, util.DefaultBackupDisableWaitRetries)
	env.EnsureVar(util.BackupStartDelaySecondsEnv, strconv.Itoa(util.DefaultBackupStartDelaySeconds))
	env.EnsureVar(util.OpsManagerMonitorAppDB, strconv.FormatBool(util.OpsManagerMonitorAppDBDefault))
}

func validateOperatorEnv(env util.OperatorEnvironment) bool {
	return slices.Contains(operatorEnvironments[:], env.String())
}

func init() {
	InitGlobalLogger()
}

func InitGlobalLogger() {
	omOperatorEnv := getOperatorEnv()

	var logger *zap.Logger
	var e error

	switch omOperatorEnv {
	case util.OperatorEnvironmentProd:
		logger, e = zap.NewProduction()
	case util.OperatorEnvironmentDev, util.OperatorEnvironmentLocal:
		// Overriding the default stacktrace behavior - have them only for errors but not for warnings
		logger, e = zap.NewDevelopment(zap.AddStacktrace(zap.ErrorLevel))
	default:
		// if for some reason we didn't set a logger, let's be safe and default to prod
		fmt.Println("No OPERATOR_ENV set, defaulting setting logger to prod")
		logger, e = zap.NewProduction()
	}

	if e != nil {
		fmt.Println("Failed to create logger, will use the default one")
		fmt.Println(e)
		// in the worst case logger might stay nil, replacing everything with a nil logger,
		// we don't want that
		logger = zap.S().Desugar()
	}

	// Set the global logger used by our operator
	zap.ReplaceGlobals(logger)
	// Set the logger for controller-runtime based on the general level of the operator
	kubelog.SetLogger(zapr.NewLogger(logger))
	// Set the logger used by telemetry package
	telemetry.ConfigureLogger()

	// Set the logger used by main.go
	log = zap.S()
}
