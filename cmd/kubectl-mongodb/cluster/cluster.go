package cluster

import (
	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/cluster/generateregistration"
	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/cluster/generateresources"
)

// ClusterCmd represents the cluster command
var ClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Configure clusters running MongoDB workloads on k8s",
	Long: `'cluster' is the toplevel command for day-2 configuration of
clusters that hold MongoDB resources.`,
}

func init() {
	ClusterCmd.AddCommand(generateresources.GenerateResourcesCmd)
	ClusterCmd.AddCommand(generateregistration.GenerateRegistrationCmd)
}
