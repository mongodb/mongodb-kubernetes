package telemetry

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/envvar"

	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/images"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
)

// Logger should default to the global default from zap. Running into the main function of this package
// should reconfigure zap.
var Logger = zap.S()

func ConfigureLogger() {
	Logger = zap.S().With("module", "Telemetry")
}

type ConfigClient interface {
	GetConfig() *rest.Config
	GetAPIReader() kubeclient.Reader
}

type LeaderRunnable struct {
	memberClusterObjectsMap map[string]cluster.Cluster
	operatorMgr             manager.Manager
	atlasClient             *Client
	currentNamespace        string
	mongodbImage            string
	databaseNonStaticImage  string
	configuredOperatorEnv   util.OperatorEnvironment
}

func (l *LeaderRunnable) NeedLeaderElection() bool {
	return true
}

func NewLeaderRunnable(operatorMgr manager.Manager, memberClusterObjectsMap map[string]cluster.Cluster, currentNamespace, mongodbImage, databaseNonStaticImage string, operatorEnv util.OperatorEnvironment) (*LeaderRunnable, error) {
	atlasClient, err := NewClient(nil)
	if err != nil {
		return nil, xerrors.Errorf("Failed creating atlas telemetry client: %w", err)
	}
	return &LeaderRunnable{
		atlasClient:             atlasClient,
		operatorMgr:             operatorMgr,
		memberClusterObjectsMap: memberClusterObjectsMap,
		currentNamespace:        currentNamespace,
		configuredOperatorEnv:   operatorEnv,

		mongodbImage:           mongodbImage,
		databaseNonStaticImage: databaseNonStaticImage,
	}, nil
}

func (l *LeaderRunnable) Start(ctx context.Context) error {
	Logger.Debug("Starting leader-only telemetry goroutine")
	RunTelemetry(ctx, l.mongodbImage, l.databaseNonStaticImage, l.currentNamespace, l.operatorMgr, l.memberClusterObjectsMap, l.atlasClient, l.configuredOperatorEnv)

	return nil
}

type snapshotCollector func(ctx context.Context, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager, operatorUUID, mongodbImage, databaseNonStaticImage string) []Event

// RunTelemetry lists the specified CRDs and sends them as events to Segment
func RunTelemetry(ctx context.Context, mongodbImage, databaseNonStaticImage, namespace string, operatorClusterMgr manager.Manager, clusterMap map[string]cluster.Cluster, atlasClient *Client, configuredOperatorEnv util.OperatorEnvironment) {
	Logger.Debug("sending telemetry!")

	intervalStr := envvar.GetEnvOrDefault(CollectionFrequency, DefaultCollectionFrequencyStr)
	duration, err := time.ParseDuration(intervalStr)
	if err != nil || duration < time.Minute {
		Logger.Warn("Failed converting %s to a duration or value is too small (minimum is one minute), using default 1h", CollectionFrequency)
		duration = DefaultCollectionFrequency
	}
	Logger.Debugf("%s is set to: %s", CollectionFrequency, duration)

	// converting to a smaller interface for better testing and clearer responsibilities
	cc := map[string]ConfigClient{}
	for s, c := range clusterMap {
		cc[s] = c
	}

	// Mapping of snapshot types to their respective collector functions
	// The functions are not 100% identical, this map takes care of that
	snapshotCollectors := map[EventType]func(ctx context.Context, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager, operatorUUID, mongodbImage, databaseNonStaticImage string) []Event{
		Operators: collectOperatorSnapshot,
		Clusters: func(ctx context.Context, cc map[string]ConfigClient, operatorClusterMgr manager.Manager, _, _, _ string) []Event {
			return collectClustersSnapshot(ctx, cc, operatorClusterMgr)
		},
		Deployments: func(ctx context.Context, _ map[string]ConfigClient, operatorClusterMgr manager.Manager, operatorUUID, mongodbImage, databaseNonStaticImage string) []Event {
			return collectDeploymentsSnapshot(ctx, operatorClusterMgr, operatorUUID, mongodbImage, databaseNonStaticImage)
		},
	}

	collectFunc := func() {
		Logger.Debug("Collecting data")

		// we are calling this per "ticker" as customers might enable RBACs after the operator has been deployed
		operatorUUID := getOrGenerateOperatorUUID(ctx, operatorClusterMgr.GetClient(), namespace)

		for eventType, f := range snapshotCollectors {
			collectAndSendSnapshot(ctx, eventType, f, cc, operatorClusterMgr, operatorUUID, mongodbImage, databaseNonStaticImage, namespace, atlasClient, configuredOperatorEnv)
		}
	}

	collectFunc()

	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			Logger.Debug("Received Shutdown; shutting down")
			return
		case <-ticker.C:
			collectFunc()
		}
	}
}

func collectAndSendSnapshot(ctx context.Context, eventType EventType, cf snapshotCollector, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager, operatorUUID, mongodbImage, databaseNonStaticImage, namespace string, atlasClient *Client, configuredOperatorEnv util.OperatorEnvironment) {
	telemetryIsEnabled := ReadBoolWithTrueAsDefault(EventTypeMappingToEnvVar[eventType])
	if !telemetryIsEnabled {
		return
	}

	Logger.Debugf("Collecting %s events!", eventType)

	events := cf(ctx, memberClusterMap, operatorClusterMgr, operatorUUID, mongodbImage, databaseNonStaticImage)
	for _, event := range events {
		event.Properties["operatorEnvironment"] = configuredOperatorEnv.String()
	}

	handleEvents(ctx, atlasClient, events, eventType, namespace, operatorClusterMgr.GetClient())
}

func collectOperatorSnapshot(ctx context.Context, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager, operatorUUID, _, _ string) []Event {
	var kubeClusterUUIDList []string
	uncachedClient := operatorClusterMgr.GetAPIReader()
	kubeClusterOperatorUUID := getKubernetesClusterUUID(ctx, uncachedClient)

	// in single cluster we don't fill the memberClusterMap
	if len(memberClusterMap) == 0 {
		memberClusterMap["single"] = operatorClusterMgr
	}

	for _, c := range memberClusterMap {
		uncachedClient := c.GetAPIReader()
		uid := getKubernetesClusterUUID(ctx, uncachedClient)
		kubeClusterUUIDList = append(kubeClusterUUIDList, uid)
	}

	slices.Sort(kubeClusterUUIDList)

	operatorEvent := OperatorUsageSnapshotProperties{
		KubernetesClusterID:  kubeClusterOperatorUUID,
		KubernetesClusterIDs: kubeClusterUUIDList,
		OperatorID:           operatorUUID,
		OperatorVersion:      versionutil.StaticContainersOperatorVersion(),
		OperatorType:         MEKO,
	}
	operatorProperties, err := maputil.StructToMap(operatorEvent)
	if err != nil {
		Logger.Debugf("failed converting properties to map: %s", err)
		return nil
	}

	return []Event{
		{
			Timestamp:  time.Now(),
			Source:     Operators,
			Properties: operatorProperties,
		},
	}
}

func collectDeploymentsSnapshot(ctx context.Context, operatorClusterMgr manager.Manager, operatorUUID, mongodbImage, databaseNonStaticImage string) []Event {
	var events []Event
	operatorClusterClient := operatorClusterMgr.GetClient()
	if operatorClusterClient == nil {
		Logger.Debug("No operatorClusterClient available, not collecting Deployments!")
		return nil
	}

	now := time.Now()
	events = append(events, getMdbEvents(ctx, operatorClusterClient, operatorUUID, mongodbImage, databaseNonStaticImage, now)...)
	events = append(events, addMultiEvents(ctx, operatorClusterClient, operatorUUID, mongodbImage, databaseNonStaticImage, now)...)
	// No need to pass databaseNonStaticImage because it is for sure not enterprise image
	events = append(events, addOmEvents(ctx, operatorClusterClient, operatorUUID, mongodbImage, now)...)
	return events
}

func getMdbEvents(ctx context.Context, operatorClusterClient kubeclient.Client, operatorUUID, mongodbImage, databaseNonStaticImage string, now time.Time) []Event {
	var events []Event
	mdbList := &mdbv1.MongoDBList{}

	if err := operatorClusterClient.List(ctx, mdbList); err != nil {
		Logger.Warnf("failed to fetch MongoDBList from Kubernetes: %v", err)
	} else {
		for _, item := range mdbList.Items {
			imageURL := mongodbImage
			if !architectures.IsRunningStaticArchitecture(item.Annotations) {
				imageURL = databaseNonStaticImage
			}

			numberOfClustersUsed := getMaxNumberOfClustersSCIsDeployedOn(item)
			properties := DeploymentUsageSnapshotProperties{
				DeploymentUID:            string(item.UID),
				OperatorID:               operatorUUID,
				Architecture:             string(architectures.GetArchitecture(item.Annotations)),
				IsMultiCluster:           item.Spec.IsMultiCluster(),
				Type:                     string(item.Spec.GetResourceType()),
				IsRunningEnterpriseImage: images.IsEnterpriseImage(imageURL),
				ExternalDomains:          getExternalDomainProperty(item),
			}

			if numberOfClustersUsed > 0 {
				properties.DatabaseClusters = ptr.To(numberOfClustersUsed)
			}

			if event := createEvent(properties, now, Deployments); event != nil {
				events = append(events, *event)
			}
		}
	}
	return events
}

func addMultiEvents(ctx context.Context, operatorClusterClient kubeclient.Client, operatorUUID, mongodbImage, databaseNonStaticImage string, now time.Time) []Event {
	var events []Event

	mdbMultiList := &mdbmultiv1.MongoDBMultiClusterList{}
	if err := operatorClusterClient.List(ctx, mdbMultiList); err != nil {
		Logger.Warnf("failed to fetch MongoDBMultiList from Kubernetes: %v", err)
	}
	for _, item := range mdbMultiList.Items {
		imageURL := mongodbImage
		if !architectures.IsRunningStaticArchitecture(item.Annotations) {
			imageURL = databaseNonStaticImage
		}

		clusters := len(item.Spec.ClusterSpecList)

		properties := DeploymentUsageSnapshotProperties{
			DatabaseClusters:         ptr.To(clusters), // cannot be null in mdbmulti
			DeploymentUID:            string(item.UID),
			OperatorID:               operatorUUID,
			Architecture:             string(architectures.GetArchitecture(item.Annotations)),
			IsMultiCluster:           true,
			Type:                     string(item.Spec.GetResourceType()),
			IsRunningEnterpriseImage: images.IsEnterpriseImage(imageURL),
			ExternalDomains:          getExternalDomainPropertyForMongoDBMulti(item),
		}

		if event := createEvent(properties, now, Deployments); event != nil {
			events = append(events, *event)
		}
	}

	return events
}

func addOmEvents(ctx context.Context, operatorClusterClient kubeclient.Client, operatorUUID, mongodbImage string, now time.Time) []Event {
	var events []Event
	omList := &omv1.MongoDBOpsManagerList{}

	if err := operatorClusterClient.List(ctx, omList); err != nil {
		Logger.Warnf("failed to fetch OMList from Kubernetes: %v", err)
	} else {
		for _, item := range omList.Items {
			// Detect enterprise
			omClusters := len(item.Spec.ClusterSpecList)
			appDBClusters := len(item.Spec.AppDB.ClusterSpecList)
			properties := DeploymentUsageSnapshotProperties{
				DeploymentUID:            string(item.UID),
				OperatorID:               operatorUUID,
				Architecture:             string(architectures.GetArchitecture(item.Annotations)),
				IsMultiCluster:           item.Spec.IsMultiCluster(),
				Type:                     "OpsManager",
				IsRunningEnterpriseImage: images.IsEnterpriseImage(mongodbImage),
				ExternalDomains:          getExternalDomainPropertyForOpsManager(item),
			}

			if omClusters > 0 {
				properties.OmClusters = ptr.To(omClusters)
			}

			if appDBClusters > 0 {
				properties.AppDBClusters = ptr.To(appDBClusters)
			}

			if event := createEvent(properties, now, Deployments); event != nil {
				events = append(events, *event)
			}
		}
	}
	return events
}

func createEvent(properties any, now time.Time, eventType EventType) *Event {
	convertedProperties, err := maputil.StructToMap(properties)
	if err != nil {
		Logger.Debugf("failed to parse %s properties: %v", eventType, err)
		return nil
	}

	return &Event{
		Timestamp:  now,
		Source:     eventType,
		Properties: convertedProperties,
	}
}

func getMaxNumberOfClustersSCIsDeployedOn(item mdbv1.MongoDB) int {
	var numberOfClustersUsed int
	if item.Spec.ConfigSrvSpec != nil {
		numberOfClustersUsed = len(item.Spec.ConfigSrvSpec.ClusterSpecList)
	}
	if item.Spec.MongosSpec != nil {
		numberOfClustersUsed = max(numberOfClustersUsed, len(item.Spec.MongosSpec.ClusterSpecList))
	}
	if item.Spec.ShardSpec != nil {
		numberOfClustersUsed = max(numberOfClustersUsed, len(item.Spec.ShardSpec.ClusterSpecList))
	}
	return numberOfClustersUsed
}

func ReadBoolWithTrueAsDefault(envVarName string) bool {
	envVar := envvar.GetEnvOrDefault(envVarName, "true")
	return strings.TrimSpace(strings.ToLower(envVar)) == "true"
}

func handleEvents(ctx context.Context, atlasClient *Client, events []Event, eventType EventType, namespace string, operatorClusterClient kubeclient.Client) {
	if err := updateTelemetryConfigMapPayload(ctx, operatorClusterClient, events, namespace, OperatorConfigMapTelemetryConfigMapName, eventType); err != nil {
		Logger.Debugf("Failed to save last collected events: %s. Not sending data", err)
		return
	}

	if sendTelemetry := ReadBoolWithTrueAsDefault(SendEnabled); !sendTelemetry {
		Logger.Debugf("Telemetry deactivated, not sending it for eventType: %s", string(eventType))
		return
	}

	isOlder, err := isTimestampOlderThanConfiguredFrequency(ctx, operatorClusterClient, namespace, OperatorConfigMapTelemetryConfigMapName, eventType)
	if err != nil {
		Logger.Debugf("Failed to check for timestamp in configmap; not sending data: %s", err)
		return
	}

	if !isOlder {
		Logger.Debugf("Not older than the configured collection interval, not sending telemetry!")
		return
	}

	err = atlasClient.SendEventWithRetry(ctx, events)
	if err == nil {
		if err := updateTelemetryConfigMapTimeStamp(ctx, operatorClusterClient, namespace, OperatorConfigMapTelemetryConfigMapName, eventType); err != nil {
			Logger.Debugf("Failed saving timestamp of successful sending of data for type: %s with error: %s", eventType, err)
		}
	} else {
		Logger.Debugf("Encountered error while trying to send payload to atlas; err: %s", err)
	}
}

func collectClustersSnapshot(ctx context.Context, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager) []Event {
	allClusterDetails := getClusterProperties(ctx, memberClusterMap, operatorClusterMgr)
	now := time.Now()

	var events []Event

	for _, properties := range allClusterDetails {
		if event := createEvent(properties, now, Clusters); event != nil {
			events = append(events, *event)
		}
	}
	return events
}

func getClusterProperties(ctx context.Context, memberClusterMap map[string]ConfigClient, operatorClusterMgr manager.Manager) []KubernetesClusterUsageSnapshotProperties {
	operatorMemberClusterProperties := detectClusterInfos(ctx, map[string]ConfigClient{"operator": operatorClusterMgr})
	memberClustersProperties := detectClusterInfos(ctx, memberClusterMap)

	uniqueProperties := make(map[string]KubernetesClusterUsageSnapshotProperties)
	for _, property := range append(operatorMemberClusterProperties, memberClustersProperties...) {
		uniqueProperties[property.KubernetesClusterID] = property
	}

	allClusterDetails := make([]KubernetesClusterUsageSnapshotProperties, 0, len(uniqueProperties))
	for _, properties := range uniqueProperties {
		allClusterDetails = append(allClusterDetails, properties)
	}

	return allClusterDetails
}

const (
	ExternalDomainMixed           = "Mixed"
	ExternalDomainClusterSpecific = "ClusterSpecific"
	ExternalDomainUniform         = "Uniform"
	ExternalDomainNone            = "None"
)

func getExternalDomainProperty(mdb mdbv1.MongoDB) string {
	isUniformExternalDomainSpecified := mdb.Spec.GetExternalDomain() != nil

	isClusterSpecificExternalDomainSpecified := isExternalDomainSpecifiedInAnyShardedClusterSpec(mdb)

	return mapExternalDomainConfigurationToEnum(isUniformExternalDomainSpecified, isClusterSpecificExternalDomainSpecified)
}

func getExternalDomainPropertyForMongoDBMulti(mdb mdbmultiv1.MongoDBMultiCluster) string {
	isUniformExternalDomainSpecified := mdb.Spec.GetExternalDomain() != nil

	isClusterSpecificExternalDomainSpecified := isExternalDomainSpecifiedInClusterSpecList(mdb.Spec.ClusterSpecList)

	return mapExternalDomainConfigurationToEnum(isUniformExternalDomainSpecified, isClusterSpecificExternalDomainSpecified)
}

func getExternalDomainPropertyForOpsManager(om omv1.MongoDBOpsManager) string {
	isUniformExternalDomainSpecified := om.Spec.AppDB.GetExternalDomain() != nil

	isClusterSpecificExternalDomainSpecified := isExternalDomainSpecifiedInClusterSpecList(om.Spec.AppDB.ClusterSpecList)

	return mapExternalDomainConfigurationToEnum(isUniformExternalDomainSpecified, isClusterSpecificExternalDomainSpecified)
}

func mapExternalDomainConfigurationToEnum(isUniformExternalDomainSpecified bool, isClusterSpecificExternalDomainSpecified bool) string {
	if isUniformExternalDomainSpecified && isClusterSpecificExternalDomainSpecified {
		return ExternalDomainMixed
	}

	if isClusterSpecificExternalDomainSpecified {
		return ExternalDomainClusterSpecific
	}

	if isUniformExternalDomainSpecified {
		return ExternalDomainUniform
	}

	return ExternalDomainNone
}

func isExternalDomainSpecifiedInAnyShardedClusterSpec(mdb mdbv1.MongoDB) bool {
	isExternalDomainSpecifiedForShard := isExternalDomainSpecifiedInShardedClusterSpec(mdb.Spec.ShardSpec)
	isExternalDomainSpecifiedForMongos := isExternalDomainSpecifiedInShardedClusterSpec(mdb.Spec.MongosSpec)
	isExternalDomainSpecifiedForConfigSrv := isExternalDomainSpecifiedInShardedClusterSpec(mdb.Spec.ConfigSrvSpec)

	return isExternalDomainSpecifiedForShard || isExternalDomainSpecifiedForMongos || isExternalDomainSpecifiedForConfigSrv
}

func isExternalDomainSpecifiedInShardedClusterSpec(shardedSpec *mdbv1.ShardedClusterComponentSpec) bool {
	if shardedSpec == nil {
		return false
	}

	return isExternalDomainSpecifiedInClusterSpecList(shardedSpec.ClusterSpecList)
}

func isExternalDomainSpecifiedInClusterSpecList(clusterSpecList mdbv1.ClusterSpecList) bool {
	if len(clusterSpecList) == 0 {
		return false
	}

	return clusterSpecList.IsExternalDomainSpecifiedInClusterSpecList()
}
