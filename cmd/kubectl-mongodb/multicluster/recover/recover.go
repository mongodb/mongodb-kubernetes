package recover

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	"k8s.io/client-go/tools/clientcmd"
)

func init() {
	RecoverCmd.Flags().StringVar(&common.MemberClusters, "member-clusters", "", "Comma separated list of member clusters. [required]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.ServiceAccount, "service-account", "mongodb-kubernetes-operator-multi-cluster", "Name of the service account which should be used for the Operator to communicate with the member clusters. [optional, default: mongodb-kubernetes-operator-multi-cluster]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.CentralCluster, "central-cluster", "", "The central cluster the operator will be deployed in. [required]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.MemberClusterNamespace, "member-cluster-namespace", "", "The namespace the member cluster resources will be deployed to. [required]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.CentralClusterNamespace, "central-cluster-namespace", "", "The namespace the Operator will be deployed to. [required]")
	RecoverCmd.Flags().BoolVar(&RecoverFlags.Cleanup, "cleanup", false, "Delete all previously created resources except for namespaces. [optional default: false]")
	RecoverCmd.Flags().BoolVar(&RecoverFlags.ClusterScoped, "cluster-scoped", false, "Create ClusterRole and ClusterRoleBindings for member clusters. [optional default: false]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.OperatorName, "operator-name", common.DefaultOperatorName, "Name used to identify the deployment of the operator. [optional, default: mongodb-kubernetes-operator]")
	RecoverCmd.Flags().BoolVar(&RecoverFlags.InstallDatabaseRoles, "install-database-roles", false, "Install the ServiceAccounts and Roles required for running database workloads in the member clusters. [optional default: false]")
	RecoverCmd.Flags().StringVar(&RecoverFlags.SourceCluster, "source-cluster", "", "The source cluster for recovery. This has to be one of the healthy member cluster that is the source of truth for new cluster configuration. [required]")
	RecoverCmd.Flags().BoolVar(&RecoverFlags.CreateServiceAccountSecrets, "create-service-account-secrets", true, "Create service account token secrets. [optional default: true]")
	RecoverCmd.Flags().StringVar(&common.MemberClustersApiServers, "member-clusters-api-servers", "", "Comma separated list of api servers addresses. [optional, default will take addresses from KUBECONFIG env var]")
}

// RecoverCmd represents the recover command
var RecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Recover the multicluster environment for MongoDB resources after a dataplane failure",
	Long: `'recover' re-configures a failed multicluster environment to a enable the shuffling of dataplane
resources to a new healthy topology.

Example:

kubectl-mongodb multicluster recover --central-cluster="operator-cluster" --member-clusters="cluster-1,cluster-3,cluster-4" --member-cluster-namespace="mongodb-fresh" --central-cluster-namespace="mongodb" --operator-name=mongodb-kubernetes-operator-multi-cluster --source-cluster="cluster-1"

`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := parseRecoverFlags(args); err != nil {
			fmt.Printf("error parsing flags: %s\n", err)
			os.Exit(1)
		}

		clientMap, err := common.CreateClientMap(RecoverFlags.MemberClusters, RecoverFlags.CentralCluster, common.LoadKubeConfigFilePath(), common.GetKubernetesClient)
		if err != nil {
			fmt.Printf("failed to create clientset map: %s", err)
			os.Exit(1)
		}

		if err := common.EnsureMultiClusterResources(cmd.Context(), RecoverFlags, clientMap); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if err := common.ReplaceClusterMembersConfigMap(cmd.Context(), clientMap[RecoverFlags.CentralCluster], RecoverFlags); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	},
}

var RecoverFlags = common.Flags{}

func parseRecoverFlags(args []string) error {
	if slices.Contains([]string{common.MemberClusters, RecoverFlags.ServiceAccount, RecoverFlags.CentralCluster, RecoverFlags.MemberClusterNamespace, RecoverFlags.CentralClusterNamespace, RecoverFlags.SourceCluster}, "") {
		return xerrors.Errorf("non empty values are required for [service-account, member-clusters, central-cluster, member-cluster-namespace, central-cluster-namespace, source-cluster]")
	}

	RecoverFlags.MemberClusters = strings.Split(common.MemberClusters, ",")
	if !slices.Contains(RecoverFlags.MemberClusters, RecoverFlags.SourceCluster) {
		return xerrors.Errorf("source-cluster has to be one of the healthy member clusters: %s", common.MemberClusters)
	}

	if strings.TrimSpace(common.MemberClustersApiServers) != "" {
		RecoverFlags.MemberClusterApiServerUrls = strings.Split(common.MemberClustersApiServers, ",")
		if len(RecoverFlags.MemberClusterApiServerUrls) != len(RecoverFlags.MemberClusters) {
			return xerrors.Errorf("expected %d addresses in member-clusters-api-servers parameter but got %d", len(RecoverFlags.MemberClusters), len(RecoverFlags.MemberClusterApiServerUrls))
		}
	}

	configFilePath := common.LoadKubeConfigFilePath()
	kubeconfig, err := clientcmd.LoadFromFile(configFilePath)
	if err != nil {
		return xerrors.Errorf("error loading kubeconfig file '%s': %w", configFilePath, err)
	}
	if len(RecoverFlags.MemberClusterApiServerUrls) == 0 {
		if RecoverFlags.MemberClusterApiServerUrls, err = common.GetMemberClusterApiServerUrls(kubeconfig, RecoverFlags.MemberClusters); err != nil {
			return err
		}
	}
	return nil
}
