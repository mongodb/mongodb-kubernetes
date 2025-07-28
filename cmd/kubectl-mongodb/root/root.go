package root

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/multicluster"
	"github.com/mongodb/mongodb-kubernetes/cmd/kubectl-mongodb/utils"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "kubectl-mongodb",
	Short: "Manage and configure MongoDB resources on k8s",
	Long: `This application is a tool to simplify maintenance tasks
of MongoDB resources in your kubernetes cluster.
	`,
}

func init() {
	rootCmd.AddCommand(multicluster.MulticlusterCmd)
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signalChan
		cancel()
	}()
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		rootCmd.Long += utils.GetBuildInfoString(buildInfo)
	}
	err := rootCmd.ExecuteContext(ctx)
	if err != nil {
		os.Exit(1)
	}
}
