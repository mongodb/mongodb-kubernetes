package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"log"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	crLog "sigs.k8s.io/controller-runtime/pkg/log"
	crZap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type flags struct {
	operatorClusterName string
	namespace           string
	operatorNamespace   string
	typeParam           string
	name                string
	watch               bool
	deployPods          bool
}

func debugCmd(ctx context.Context) error {
	flags := flags{}
	flagSet := flag.NewFlagSet("debug", flag.ExitOnError)
	flagSet.StringVar(&flags.operatorClusterName, "context", "", "Operator context name")
	flagSet.StringVar(&flags.namespace, "namespace", os.Getenv("NAMESPACE"), "Namespace of the resource, default value from NAMESPACE env var")
	flagSet.StringVar(&flags.operatorNamespace, "operator-namespace", os.Getenv("NAMESPACE"), "Namespace of the operator, default value from NAMESPACE env var")
	flagSet.StringVar(&flags.typeParam, "type", "", "Type od crd: om, mdb, mdbmc. Optional if --watch is specified.")
	flagSet.StringVar(&flags.name, "name", "", "Name of the resource")
	flagSet.BoolVar(&flags.deployPods, "deployPods", false, "Specify if all debug pods should be deployed immediately (all debug statefulsets are scaled to 1). If not specified the statefulsets are scaled to zero, so no debug pods are deployed.")
	flagSet.BoolVar(&flags.watch, "watch", false, "Specify to run in the operator mode, i.e. watch the resource for changes and deploy new debugging pods")
	flagSet.StringVar(&flags.name, "viewLogDir", "", "Log directory to render tmux session for")
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return err
	}

	if !flags.watch && (flags.operatorClusterName == "" || flags.typeParam == "" || flags.name == "") {
		fmt.Print(`mdbdebug is deploying debug pods for realtime observability.

This utility is only watching MongoDB and MongoDBOpsManager resources and prepares the deployment of debug pods.
Debug pod, when it's spawned for each or a specified database/OM pod is then taking role of monitoring its resources (jsons, automation config, etc.) for changes.
Hook into debug pods with attach.sh script.

There are two modes of operation:
  - In watch mode (-watch) it's working as an operator hooking watching all MongoDB and MongoDBOpsManager CRs and reacting to changes in the topology.
    Basically the operator mode is executing the single resource mode on any resource change and it's automatically figuring out necessary parameters.
  - In single resource mode you must specify exact details of the resource you want to debug: type (-type), name (-name).
    It executes once, deploys all necessary debug resources, dumps commands to attach to the debug pods and exits.

Examples:
  $ mdbdebug

`)
		flagSet.Usage()
		return xerrors.Errorf("missing arguments")
	}

	return debug(ctx, flags)
}

func getMemberClusters(operatorClusterName string, c client.Client, namespace string) ([]string, error) {
	m := corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{Name: util.MemberListConfigMapName, Namespace: namespace}, &m)
	if err != nil {
		return []string{operatorClusterName}, err
	}

	members := []string{operatorClusterName}
	for member := range m.Data {
		members = append(members, member)
	}

	return members, nil
}

func debug(ctx context.Context, flags flags) error {
	kubeConfigPath := LoadKubeConfigFilePath()
	log.Printf("Creating k8s client from %s for context %s", kubeConfigPath, flags.operatorClusterName)

	var operatorClusterMap map[string]client.Client
	var operatorConfigMap map[string]*rest.Config

	runningInCluster := false
	if inClusterConfig, err := rest.InClusterConfig(); err == nil {
		operatorClusterMap, operatorConfigMap, err = createOperatorClusterMapFromInClusterConfig(flags.operatorClusterName, inClusterConfig)
		runningInCluster = true
	} else {
		operatorClusterMap, operatorConfigMap, err = createClusterMap([]string{flags.operatorClusterName}, kubeConfigPath)
		if err != nil {
			return xerrors.Errorf("failed to initialize client for the operator cluster %s from kubeconfig %s: %w", flags.operatorClusterName, kubeConfigPath, err)
		}
	}

	operatorClient, ok := operatorClusterMap[flags.operatorClusterName]
	if !ok {
		return xerrors.Errorf("failed to initialize central cluster %s client", flags.operatorClusterName)
	}

	clusterMap := operatorClusterMap
	if !runningInCluster {
		clusterNames, err := getMemberClusters(flags.operatorClusterName, operatorClient, flags.operatorNamespace)
		if err != nil {
			if errors.IsNotFound(err) {
				clusterNames = []string{flags.operatorClusterName}
			} else {
				return xerrors.Errorf("failed to get cluster names from the config map from cluster %s and namespace %s: %w", flags.operatorClusterName, flags.operatorNamespace, err)
			}
		}

		clusterMap, _, err = createClusterMap(clusterNames, kubeConfigPath)
		if err != nil {
			return xerrors.Errorf("failed to initialize client map for cluster names %v from kubeconfig %s: %w", clusterNames, kubeConfigPath, err)
		}
	}

	if flags.watch {
		return deployDebugWithWatch(ctx, flags.operatorClusterName, operatorConfigMap, flags.typeParam, flags.namespace, flags.operatorNamespace, flags.name, clusterMap, flags.deployPods)
	} else {
		return deployDebugWithoutWatch(ctx, flags.operatorClusterName, flags.typeParam, flags.namespace, flags.operatorNamespace, flags.name, clusterMap, flags.deployPods)
	}
}

func deployDebugWithWatch(ctx context.Context, operatorClusterName string, configMap map[string]*rest.Config, resourceType string, namespace string, operatorNamespace string, resourceName string, clusterMap map[string]client.Client, deployPods bool) error {
	crLog.SetLogger(crZap.New())

	mgr, err := manager.New(configMap[operatorClusterName], manager.Options{
		Scheme:                 CurrentScheme(),
		Metrics:                server.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		return xerrors.Errorf("cannot create manager: %w", err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&mdbv1.MongoDB{}).
		Watches(&mdbv1.MongoDB{}, &handler.EnqueueRequestForObject{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(newMongoDBReconciler(operatorClusterName, namespace, clusterMap, deployPods))
	if err != nil {
		return xerrors.Errorf("error building MongoDB controller: %w", err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&omv1.MongoDBOpsManager{}).
		Watches(&omv1.MongoDBOpsManager{}, &handler.EnqueueRequestForObject{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(newOpsManagerReconciler(operatorClusterName, namespace, clusterMap, deployPods))
	if err != nil {
		return xerrors.Errorf("error building MongoDB controller: %w", err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&mdbcv1.MongoDBCommunity{}).
		Watches(&mdbcv1.MongoDBCommunity{}, &handler.EnqueueRequestForObject{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(newMongoDBCommunityReconciler(operatorClusterName, namespace, clusterMap[operatorClusterName], deployPods))
	if err != nil {
		return xerrors.Errorf("error building MongoDBCommunity controller: %w", err)
	}

	err = builder.ControllerManagedBy(mgr).
		For(&searchv1.MongoDBSearch{}).
		Watches(&searchv1.MongoDBSearch{}, &handler.EnqueueRequestForObject{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(newMongoDBSearchReconciler(operatorClusterName, namespace, clusterMap[operatorClusterName], deployPods))
	if err != nil {
		return xerrors.Errorf("error building MongoDBCommunity controller: %w", err)
	}

	if err := mgr.Start(ctx); err != nil {
		return xerrors.Errorf("error starting controller: %w", err)
	}

	return nil
}

type attachCommand struct {
	Command         string `json:"command,omitempty"`
	ShortName       string `json:"shortName,omitempty"`
	PodName         string `json:"podName,omitempty"`
	DebugPodName    string `json:"debugPodName,omitempty"`
	DebugStsName    string `json:"debugStsName,omitempty"`
	ResourceType    string `json:"resourceType,omitempty"`
	ResourceName    string `json:"resourceName,omitempty"`
	OperatorContext string `json:"operatorContext,omitempty"`
	DebugPodContext string `json:"debugPodContext,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
}

func createOrUpdateAttachCommandsCM(ctx context.Context, logger *zap.SugaredLogger, resourceNamespace string, resourceName string, resourceType string, attachCommands []attachCommand, operatorClient client.Client) error {
	attachCommandsBytes, err := json.Marshal(attachCommands)
	if err != nil {
		return nil
	}
	attachCommandsData := map[string]string{
		"commands":     string(attachCommandsBytes),
		"resourceType": resourceType,
		"resourceName": resourceName,
	}
	attachCommandsCM := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: resourceNamespace,
			Name:      fmt.Sprintf("mdb-debug-attach-commands-%s-%s", resourceType, resourceName),
			Labels:    mdbDebugLabels,
		},
		Data: attachCommandsData,
	}
	cmName := types.NamespacedName{Namespace: attachCommandsCM.ObjectMeta.Namespace, Name: attachCommandsCM.ObjectMeta.Name}
	err = operatorClient.Get(ctx, cmName, &attachCommandsCM)
	if err != nil && !errors.IsNotFound(err) {
		return xerrors.Errorf("error getting MongoDB resource %s: %w", attachCommandsCM.Name, err)
	}

	if errors.IsNotFound(err) {
		if err := operatorClient.Create(ctx, &attachCommandsCM); err != nil {
			return xerrors.Errorf("error creating attach commands config map %s: %w", attachCommandsCM.Name, err)
		}
	} else {
		attachCommandsCM.Data = attachCommandsData
		if err := operatorClient.Update(ctx, &attachCommandsCM); err != nil {
			return xerrors.Errorf("error updating attach commands config map %s: %w", attachCommandsCM.Name, err)
		}
	}

	logger.Debugf("Saved attach commands to %s config map:\n%s", attachCommandsCM.Name, attachCommandsData["commands"])
	return nil
}

func deployDebugWithoutWatch(ctx context.Context, operatorClusterName string, resourceType string, namespace string, operatorNamespace string, resourceName string, clusterMap map[string]client.Client, deployPods bool) error {
	logger := zap.S()
	var err error
	var attachCommands []attachCommand
	switch resourceType {
	case "om":
		attachCommands, err = debugOpsManager(ctx, clusterMap, operatorClusterName, namespace, resourceName, deployPods)
	case "mdb":
		attachCommands, err = debugMongoDB(ctx, clusterMap, operatorClusterName, namespace, resourceName, deployPods)
	case "mdbc":
		attachCommands, err = debugMongoDBCommunity(ctx, namespace, resourceName, operatorClusterName, kubernetesClient.NewClient(clusterMap[operatorClusterName]), deployPods)
	case "mdbs":
		attachCommands, err = debugMongoDBSearch(ctx, namespace, resourceName, operatorClusterName, kubernetesClient.NewClient(clusterMap[operatorClusterName]), deployPods)
	}

	err = createOrUpdateAttachCommandsCM(ctx, logger, namespace, resourceName, resourceType, attachCommands, clusterMap[operatorClusterName])

	return err
}

func createKubectlAttachCommand(operatorClusterName string, memberClusterName string, namespace string, podName string, debugPodName string) string {
	contextName := memberClusterName
	if memberClusterName == multicluster.LegacyCentralClusterName {
		contextName = operatorClusterName
	}
	return fmt.Sprintf(`kubectl --context %s --namespace %s -it exec %s -- tmux attach`, contextName, namespace, debugPodName)
}

func debugOpsManager(ctx context.Context, clusterMap map[string]client.Client, operatorClusterName, namespace, name string, deployPods bool) ([]attachCommand, error) {
	centralClusterClient := clusterMap[operatorClusterName]

	opsManager := omv1.MongoDBOpsManager{}
	if err := centralClusterClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &opsManager); err != nil {
		return nil, xerrors.Errorf("error getting resource %s/%s", namespace, name)
	}

	appDB := opsManager.Spec.AppDB
	commonController := operator.NewReconcileCommonController(ctx, centralClusterClient)

	appDBReconciler, err := operator.NewAppDBReplicaSetReconciler(ctx, nil, "", appDB, commonController, nil, opsManager.Annotations, clusterMap, zap.S())
	if err != nil {
		return nil, err
	}

	var attachCommands []attachCommand
	for _, memberCluster := range appDBReconciler.GetHealthyMemberClusters() {
		fmt.Printf("appdb member cluster: %+v\n", memberCluster)
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, namespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if appDBAttachCommands, err := debugAppDB(ctx, &opsManager, operatorClusterName, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug appdb %s/%s in cluster %s: %w", namespace, appDB.Name(), memberCluster.Name, err)
		} else {
			attachCommands = append(attachCommands, appDBAttachCommands...)
		}
	}

	centralClient := kubernetesClient.NewClient(centralClusterClient)

	opsManagerReconciler := operator.NewOpsManagerReconciler(ctx, centralClient, clusterMap, nil, "", "", nil, nil, nil)

	omReconcilerHelper, err := operator.NewOpsManagerReconcilerHelper(ctx, opsManagerReconciler, &opsManager, clusterMap, zap.S())
	if err != nil {
		return nil, xerrors.Errorf("failed to create NewOpsManagerReconcilerHelper: %w", err)
	}

	for _, memberCluster := range omReconcilerHelper.GetMemberClusters() {
		fmt.Printf("om member cluster: %+v\n", memberCluster)
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, namespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if omAttachCommands, err := debugOM(ctx, &opsManager, *omReconcilerHelper, operatorClusterName, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug appdb %s/%s in cluster %s: %w", namespace, appDB.Name(), memberCluster.Name, err)
		} else {
			attachCommands = append(attachCommands, omAttachCommands...)
		}
	}

	return attachCommands, nil
}

func debugMongoDB(ctx context.Context, clusterMap map[string]client.Client, operatorClusterName, namespace, name string, deployPods bool) ([]attachCommand, error) {
	centralClusterClient := clusterMap[operatorClusterName]

	mdb := mdbv1.MongoDB{}
	if err := centralClusterClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &mdb); err != nil {
		return nil, xerrors.Errorf("error getting resource %s/%s", namespace, name)
	}

	switch mdb.Spec.ResourceType {
	case mdbv1.ShardedCluster:
		if attachCommands, err := debugShardedCluster(ctx, &mdb, operatorClusterName, clusterMap, deployPods); err != nil {
			return nil, err
		} else {
			return attachCommands, nil
		}
	case mdbv1.ReplicaSet:
		if mdb.Spec.GetTopology() == "MultiCluster" {
			if attachCommands, err := debugMultiReplicaSet(ctx, mdb.Namespace, mdb.Name, &mdb.Spec.DbCommonSpec, mdb.Annotations, operatorClusterName, clusterMap, deployPods); err != nil {
				return nil, err
			} else {
				return attachCommands, nil
			}
		} else {
			if attachCommands, err := debugReplicaSet(ctx, mdb.Namespace, mdb.Name, &mdb.Spec.DbCommonSpec, mdb.Annotations, mdb.Spec.Replicas(), operatorClusterName, clusterMap, deployPods); err != nil {
				return nil, err
			} else {
				return attachCommands, nil
			}
		}
	default:
		panic("not implemented")
	}
}

func getHealthyMemberClusters(memberClusters []multicluster.MemberCluster) []multicluster.MemberCluster {
	var result []multicluster.MemberCluster
	for i := range memberClusters {
		if memberClusters[i].Healthy {
			result = append(result, memberClusters[i])
		}
	}

	return result
}

func debugShardedCluster(ctx context.Context, mdb *mdbv1.MongoDB, operatorClusterName string, clusterMap map[string]client.Client, deployPods bool) ([]attachCommand, error) {
	commonController := operator.NewReconcileCommonController(ctx, clusterMap[operatorClusterName])
	reconcilerHelper, err := operator.NewShardedClusterReconcilerHelper(ctx, commonController, nil, "", "", true, false, mdb, clusterMap, om.NewOpsManagerConnection, zap.S())
	if err != nil {
		return nil, err
	}
	var allAttachCommands []attachCommand
	for _, memberCluster := range getHealthyMemberClusters(reconcilerHelper.MongosMemberClusters()) {
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, mdb.Namespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if attachCommands, err := debugMongos(ctx, mdb, operatorClusterName, reconcilerHelper, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug MongoDB mongos %s/%s in cluster %s: %w", mdb.Namespace, mdb.Name, memberCluster.Name, err)
		} else {
			allAttachCommands = append(allAttachCommands, attachCommands...)
		}
	}

	for _, memberCluster := range getHealthyMemberClusters(reconcilerHelper.ConfigSrvMemberClusters()) {
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, mdb.Namespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if attachCommands, err := debugConfigServers(ctx, mdb, operatorClusterName, reconcilerHelper, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug MongoDB mongos %s/%s in cluster %s: %w", mdb.Namespace, mdb.Name, memberCluster.Name, err)
		} else {
			allAttachCommands = append(allAttachCommands, attachCommands...)
		}
	}

	for shardIdx := 0; shardIdx < len(reconcilerHelper.DesiredShardsConfiguration()); shardIdx++ {
		for _, memberCluster := range getHealthyMemberClusters(reconcilerHelper.ShardsMemberClustersMap()[shardIdx]) {
			if err := createServiceAccountAndRoles(ctx, memberCluster.Client, mdb.Namespace); err != nil {
				return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
			}

			if attachCommands, err := debugShardsServers(ctx, mdb, operatorClusterName, reconcilerHelper, shardIdx, memberCluster, deployPods); err != nil {
				return nil, xerrors.Errorf("failed to debug MongoDB mongos %s/%s in cluster %s: %w", mdb.Namespace, mdb.Name, memberCluster.Name, err)
			} else {
				allAttachCommands = append(allAttachCommands, attachCommands...)
			}
		}
	}

	return allAttachCommands, nil
}

func debugReplicaSet(ctx context.Context, resourceNamespace string, resourceName string, mdb *mdbv1.DbCommonSpec, mdbAnnotations map[string]string, singleClusterMembers int, operatorClusterName string, clusterMap map[string]client.Client, deployPods bool) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for _, memberCluster := range getHealthyMemberClusters(getReplicaSetMemberClusters(mdb, singleClusterMembers, clusterMap, operatorClusterName)) {
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, resourceNamespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if replicaSetAttachCommands, err := debugReplicaSetPods(ctx, resourceNamespace, resourceName, mdb, mdbAnnotations, operatorClusterName, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug MongoDB mongos %s/%s in cluster %s: %w", resourceNamespace, resourceName, memberCluster.Name, err)
		} else {
			attachCommands = append(attachCommands, replicaSetAttachCommands...)
		}
	}

	return attachCommands, nil
}

func debugMultiReplicaSet(ctx context.Context, resourceNamespace string, resourceName string, dbCommonSpec *mdbv1.DbCommonSpec, mdbAnnotations map[string]string, operatorClusterName string, clusterMap map[string]client.Client, deployPods bool) ([]attachCommand, error) {
	var attachCommands []attachCommand
	// FIXME singleClusterMembers
	for _, memberCluster := range getHealthyMemberClusters(getReplicaSetMemberClusters(dbCommonSpec, 0, clusterMap, operatorClusterName)) {
		if err := createServiceAccountAndRoles(ctx, memberCluster.Client, resourceNamespace); err != nil {
			return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", memberCluster.Name, err)
		}

		if replicaSetAttachCommands, err := debugReplicaSetPods(ctx, resourceNamespace, resourceName, dbCommonSpec, mdbAnnotations, operatorClusterName, memberCluster, deployPods); err != nil {
			return nil, xerrors.Errorf("failed to debug MongoDB mongos %s/%s in cluster %s: %w", resourceNamespace, resourceName, memberCluster.Name, err)
		} else {
			attachCommands = append(attachCommands, replicaSetAttachCommands...)
		}
	}

	return attachCommands, nil
}

func getReplicaSetMemberClusters(mdb *mdbv1.DbCommonSpec, singleClusterMembers int, clusterMap map[string]client.Client, operatorClusterName string) []multicluster.MemberCluster {
	if mdb.Topology != mdbv1.ClusterTopologyMultiCluster {
		kubeClient := kubernetesClient.NewClient(clusterMap[operatorClusterName])
		legacyCluster := multicluster.GetLegacyCentralMemberCluster(singleClusterMembers, 0, kubeClient, secrets.SecretClient{
			VaultClient: nil,
			KubeClient:  kubeClient,
		})
		return []multicluster.MemberCluster{legacyCluster}
	} else {

	}

	panic("Not implemented")
	return nil
}
