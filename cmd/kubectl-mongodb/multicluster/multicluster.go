package multicluster

import (
	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/multicluster/recover"
	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/multicluster/setup"
	
	"github.com/spf13/cobra"
)

// MulticlusterCmd represents the multicluster command
var MulticlusterCmd = &cobra.Command{
	Use:   "multicluster",
	Short: "Manage MongoDB multicluster environments on k8s",
	Long: `'multicluster' is the toplevel command for managing
multicluster environments that hold MongoDB resources.`,
}

func init() {
	MulticlusterCmd.AddCommand(setup.SetupCmd)
	MulticlusterCmd.AddCommand(recover.RecoverCmd)
}
