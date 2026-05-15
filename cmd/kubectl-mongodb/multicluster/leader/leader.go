package leader

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

var namespace string

// Cmd is the cobra command exported for registration by the multicluster root.
var Cmd = &cobra.Command{
	Use:   "leader",
	Short: "Print the current Raft leader cluster for the MCK multi-cluster operator.",
	Long: `Reads the raft-leader ConfigMap from the first reachable kubeconfig context
and prints the cluster name to use for kubectl apply.`,
	Run: func(cmd *cobra.Command, _ []string) {
		if err := run(cmd.Context()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	},
}

func init() {
	Cmd.Flags().StringVar(&namespace, "namespace", "mongodb", "Operator namespace to read raft-leader from.")
}

func run(ctx context.Context) error {
	configPath := common.LoadKubeConfigFilePath()
	kc, err := clientcmd.LoadFromFile(configPath)
	if err != nil {
		return xerrors.Errorf("loading kubeconfig %s: %w", configPath, err)
	}

	for ctxName := range kc.Contexts {
		cli, err := buildClient(kc, ctxName)
		if err != nil {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		cm, err := cli.CoreV1().ConfigMaps(namespace).Get(c, "raft-leader", metav1.GetOptions{})
		cancel()
		if err != nil {
			continue
		}
		v, ok := cm.Data["clusterName"]
		if !ok || v == "" {
			continue
		}
		fmt.Println(v)
		return nil
	}
	return xerrors.Errorf("no reachable cluster has a raft-leader ConfigMap with clusterName set")
}

func buildClient(kc *clientcmdapi.Config, ctxName string) (*kubernetes.Clientset, error) {
	cc := clientcmd.NewDefaultClientConfig(*kc, &clientcmd.ConfigOverrides{CurrentContext: ctxName})
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}
