package main

import (
	"fmt"
	"log"

	"github.com/10gen/ops-manager-kubernetes/production_notes/provisioner"
	flag "github.com/spf13/pflag"
)

const (
	small  string = "M30"
	medium string = "M80"
	large  string = "M300"

	// custom is used when provisioning a cluster for custom tests that do not
	// require instances size comparable with Atlas
	custom      string = "custom"
	clusterName string = "loadtesting.mongokubernetes.com"
)

type provisioningOpts struct {
	size                 string
	delete               bool
	wait                 bool
	kubeConfigExportFile string
}

func parseArgs() provisioningOpts {
	opts := provisioningOpts{}
	flag.StringVar(&opts.size, "size", "", "Size of the cluster {M30,M80,M300,custom}")
	flag.BoolVar(&opts.delete, "delete", false, "Delete the cluster before running, if it exists")
	flag.BoolVar(&opts.wait, "wait", false, "Wait for the cluster to be ready")
	flag.StringVar(&opts.kubeConfigExportFile, "save-kube-config", "~/.kube/config", "Export kubeconfig file to the specified location")
	flag.Parse()
	return opts
}

func clusterSizeToNodeInstanceSize(size string) (string, error) {
	switch size {
	case small:
		return "m5.large", nil
	case medium:
		return "m5.4xlarge", nil
	case large:
		return "m5.24xlarge", nil
	case custom:
		return "t2.2xlarge", nil
	default:
		return "", fmt.Errorf("got an invalid cluster size: %s", size)
	}
}

func main() {
	opts := parseArgs()

	if opts.delete {
		err := provisioner.DeleteIfExists(clusterName)
		if err != nil {
			log.Fatalf("Can't execute delete command: %s", err)
		}
	}

	nodeInstanceSize, err := clusterSizeToNodeInstanceSize(opts.size)
	if err != nil {
		log.Fatalf("Error in processing arguments: %s", err)
	}

	err = provisioner.CreateCluster(clusterName, nodeInstanceSize)
	if err != nil {
		log.Fatalf("Can't create kops cluster: %s", err)
	}

	if opts.wait {
		err := provisioner.WaitForClusterToBeReady(clusterName)
		if err != nil {
			log.Fatalf("Error in waiting for the cluster to be ready %s", err)
		}
	}

	err = provisioner.ExportKubecfg(clusterName, opts.kubeConfigExportFile)
	if err != nil {
		log.Fatalf("Error in exporting cluster kubecfg %s", err)
	}

}
