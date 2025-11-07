package main

import (
	"context"
	"flag"
	"fmt"
	golog "log"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/zapr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
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
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	localruntime "runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	runtime_cluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	kubelog "sigs.k8s.io/controller-runtime/pkg/log"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	crWebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	mcov1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	mcoController "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
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
	clusterMongoDBRoleCRDPlural  = "clustermongodbroles"
)

var (
	log             *zap.SugaredLogger
	operatorEnvOnce sync.Once

	// List of allowed operator environments. The first element of this list is
	// considered the default one.
	operatorEnvironments = []string{util.OperatorEnvironmentDev.String(), util.OperatorEnvironmentLocal.String(), util.OperatorEnvironmentProd.String()}

	scheme = runtime.NewScheme()

	// Default CRDs to watch (if not specified on the command line)
	crds crdsToWatch
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiv1.AddToScheme(scheme))
	utilruntime.Must(mcov1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme

	flag.Var(&crds, "watch-resource", "A Watch Resource specifies if the Operator should watch the given resource")
}

// crdsToWatch is a custom Value implementation which can be
// used to receive command line arguments
type crdsToWatch []string

func (c *crdsToWatch) Set(value string) error {
	*c = append(*c, strings.ToLower(value))
	return nil
}

func (c *crdsToWatch) String() string {
	return strings.Join(*c, ",")
}

func main() {
	flag.Parse()
	// If no CRDs are specified, we set default to non-multicluster CRDs
	if len(crds) == 0 {
		crds = crdsToWatch{
			mongoDBCRDPlural,
			mongoDBUserCRDPlural,
			mongoDBOpsManagerCRDPlural,
			mongoDBCommunityCRDPlural,
			mongoDBSearchCRDPlural,
			clusterMongoDBRoleCRDPlural,
		}
	}

	ctx := signals.SetupSignalHandler()
	operator.OmUpdateChannel = make(chan event.GenericEvent)

	klog.InitFlags(nil)
	initializeEnvironment()

	imageUrls := images.LoadImageUrlsFromEnv()
	forceEnterprise := env.ReadBoolOrDefault(architectures.MdbAssumeEnterpriseImage, false)
	initDatabaseNonStaticImageVersion := env.ReadOrDefault(construct.InitDatabaseVersionEnv, "latest")
	databaseNonStaticImageVersion := env.ReadOrDefault(construct.DatabaseVersionEnv, "latest")
	initAppdbVersion := env.ReadOrDefault(construct.InitAppdbVersionEnv, "latest")
	initOpsManagerImageVersion := env.ReadOrDefault(util.InitOpsManagerVersion, "latest")
	// Namespace where the operator is installed
	currentNamespace := env.ReadOrPanic(util.CurrentNamespace)
	webhookSVCSelector := env.ReadOrPanic(util.OperatorNameEnv)

	enableClusterMongoDBRoles := slices.Contains(crds, clusterMongoDBRoleCRDPlural)

	// Get trace and span IDs from environment variables
	traceIDHex := os.Getenv("OTEL_TRACE_ID")
	spanIDHex := os.Getenv("OTEL_PARENT_ID")
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	// Get a config to talk to the apiserver
	cfg := ctrl.GetConfigOrDie()

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
		log.Fatal(err)
	}
	log.Info("Registering Components.")

	// Setup Scheme for all resources
	if err := apiv1.AddToScheme(scheme); err != nil {
		log.Fatal(err)
	}

	// memberClusterObjectsMap is a map of clusterName -> clusterObject
	memberClusterObjectsMap := make(map[string]runtime_cluster.Cluster)

	if slices.Contains(crds, mongoDBMultiClusterCRDPlural) {
		memberClustersNames, err := getMemberClusters(ctx, cfg, currentNamespace)
		if err != nil {
			log.Fatal(err)
		}

		log.Infof("Watching Member clusters: %s", memberClustersNames)

		if len(memberClustersNames) == 0 {
			log.Warnf("The operator did not detect any member clusters")
		}

		memberClusterClients, err := multicluster.CreateMemberClusterClients(memberClustersNames, multicluster.GetKubeConfigPath())
		if err != nil {
			log.Fatal(err)
		}

		// Add the cluster object to the manager corresponding to each member clusters.
		for k, v := range memberClusterClients {
			var cluster runtime_cluster.Cluster

			cluster, err := runtime_cluster.New(v, func(options *runtime_cluster.Options) {
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
				log.Fatal(err)
			}
		}
	}

	// Setup all Controllers
	if slices.Contains(crds, mongoDBCRDPlural) {
		if err := setupMongoDBCRD(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, memberClusterObjectsMap); err != nil {
			log.Fatal(err)
		}
	}
	if slices.Contains(crds, mongoDBOpsManagerCRDPlural) {
		if err := setupMongoDBOpsManagerCRD(ctx, mgr, memberClusterObjectsMap, imageUrls, initAppdbVersion, initOpsManagerImageVersion); err != nil {
			log.Fatal(err)
		}
	}
	if slices.Contains(crds, mongoDBUserCRDPlural) {
		if err := setupMongoDBUserCRD(ctx, mgr, memberClusterObjectsMap); err != nil {
			log.Fatal(err)
		}
	}
	if slices.Contains(crds, mongoDBMultiClusterCRDPlural) {
		if err := setupMongoDBMultiClusterCRD(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, memberClusterObjectsMap); err != nil {
			log.Fatal(err)
		}
	}
	if slices.Contains(crds, mongoDBSearchCRDPlural) {
		if err := setupMongoDBSearchCRD(ctx, mgr); err != nil {
			log.Fatal(err)
		}
	}

	for _, r := range crds {
		log.Infof("Registered CRD: %s", r)
	}

	if slices.Contains(crds, mongoDBCommunityCRDPlural) {
		if err := setupCommunityController(
			ctx,
			mgr,
			envvar.GetEnvOrDefault(mcoConstruct.MongodbCommunityRepoUrlEnv, "quay.io/mongodb"),
			// when running MCO resource -> mongodb-community-server
			// when running appdb -> mongodb-enterprise-server
			env.ReadOrPanic(mcoConstruct.MongodbCommunityImageEnv),
			envvar.GetEnvOrDefault(mcoConstruct.MongoDBCommunityImageTypeEnv, mcoConstruct.DefaultImageType),
			env.ReadOrPanic(util.MongodbCommunityAgentImageEnv),
			env.ReadOrPanic(mcoConstruct.VersionUpgradeHookImageEnv),
			env.ReadOrPanic(mcoConstruct.ReadinessProbeImageEnv),
		); err != nil {
			log.Fatal(err)
		}
	}

	if telemetry.IsTelemetryActivated() {
		log.Info("Running telemetry component!")
		telemetryRunnable, err := telemetry.NewLeaderRunnable(mgr, memberClusterObjectsMap, currentNamespace, imageUrls[mcoConstruct.MongodbImageEnv], imageUrls[util.NonStaticDatabaseEnterpriseImage], getOperatorEnv())
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

	if err := mgr.Start(ctx); err != nil {
		log.Fatal(err)
	}
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

func setupMongoDBCRD(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool, enableClusterMongoDBRoles bool, memberClusterObjectsMap map[string]runtime_cluster.Cluster) error {
	if err := operator.AddStandaloneController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles); err != nil {
		return err
	}
	if err := operator.AddReplicaSetController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles); err != nil {
		return err
	}
	if err := operator.AddShardedClusterController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, memberClusterObjectsMap); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&mdbv1.MongoDB{}).Complete()
}

func setupMongoDBOpsManagerCRD(ctx context.Context, mgr manager.Manager, memberClusterObjectsMap map[string]runtime_cluster.Cluster, imageUrls images.ImageUrls, initAppdbVersion, initOpsManagerImageVersion string) error {
	if err := operator.AddOpsManagerController(ctx, mgr, memberClusterObjectsMap, imageUrls, initAppdbVersion, initOpsManagerImageVersion); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&omv1.MongoDBOpsManager{}).Complete()
}

func setupMongoDBUserCRD(ctx context.Context, mgr manager.Manager, memberClusterObjectsMap map[string]runtime_cluster.Cluster) error {
	return operator.AddMongoDBUserController(ctx, mgr, memberClusterObjectsMap)
}

func setupMongoDBMultiClusterCRD(ctx context.Context, mgr manager.Manager, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, forceEnterprise bool, enableClusterMongoDBRoles bool, memberClusterObjectsMap map[string]runtime_cluster.Cluster) error {
	if err := operator.AddMultiReplicaSetController(ctx, mgr, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, forceEnterprise, enableClusterMongoDBRoles, memberClusterObjectsMap); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).For(&mdbmultiv1.MongoDBMultiCluster{}).Complete()
}

func setupMongoDBSearchCRD(ctx context.Context, mgr manager.Manager) error {
	return operator.AddMongoDBSearchController(ctx, mgr, searchcontroller.OperatorSearchConfig{
		SearchRepo:    env.ReadOrPanic("MDB_SEARCH_REPO_URL"),
		SearchName:    env.ReadOrPanic("MDB_SEARCH_NAME"),
		SearchVersion: env.ReadOrPanic("MDB_SEARCH_VERSION"),
	})
}

func setupCommunityController(
	ctx context.Context,
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
	return operatorEnvironments[1] == env.ReadOrPanic(util.OmOperatorEnv)
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
			golog.Printf("Using default environment, %s, instead", util.OperatorEnvironmentDev)
		})
		operatorEnv = util.OperatorEnvironmentDev
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
	env.EnsureVar(util.BackupDisableWaitSecondsEnv, util.DefaultBackupDisableWaitSeconds)
	env.EnsureVar(util.BackupDisableWaitRetriesEnv, util.DefaultBackupDisableWaitRetries)
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
