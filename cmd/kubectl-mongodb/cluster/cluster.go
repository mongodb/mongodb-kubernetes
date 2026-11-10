package cluster

import (
	"github.com/spf13/cobra"

	generatememberregistration "github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/cluster/generateregistration"
	generatememberresources "github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/cluster/generateresources"
)

// MulticlusterCmd represents the multicluster command
var MulticlusterCmd = &cobra.Command{
	Use:   "multicluster",
	Short: "Manage MongoDB multicluster environments on k8s",
	Long: `'multicluster' is the toplevel command for managing
multicluster environments that hold MongoDB resources.`,
}

func init() {
	MulticlusterCmd.AddCommand(generatememberresources.GenerateMemberResourcesCmd)
	MulticlusterCmd.AddCommand(generatememberregistration.GenerateMemberRegistrationCmd)
}
