package provisioner

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

func checkClusterExists(clusterName string) (bool, error) {
	cmd := exec.Command("kops", "get", "cluster", clusterName)
	out, err := cmd.CombinedOutput()
	if err == nil {
		// No error means kops get cluster completed succesfully
		return true, nil
	}
	// Need to dinstinguish between error "cluster not found"
	// and other errors
	if strings.Contains(string(out), "cluster not found") {
		return false, nil
	}
	return false, err
}

func execWithOutputAndReturnError(cmd *exec.Cmd) error {
	log.Print(cmd.String())
	out, err := cmd.CombinedOutput()
	log.Print(string(out))
	return err
}

func DeleteIfExists(clusterName string) error {
	clusterExists, err := checkClusterExists(clusterName)
	if err != nil {
		return err
	}
	if !clusterExists {
		return nil
	}
	log.Printf("Deleting cluster %s", clusterName)
	cmd := exec.Command("kops", "delete", "cluster", clusterName, "--yes")
	return execWithOutputAndReturnError(cmd)
}

func CreateCluster(clusterName string, nodeSize string, networking string) error {
	var cni string
	if networking != "" {
		cni = fmt.Sprintf("--networking=%s", networking)
	}

	createCmd := exec.Command("kops", "create", "cluster", clusterName, "--node-size", nodeSize, "--zones=eu-west-2a", "--node-count=4", "--node-volume-size=40", "--master-size=t2.medium", "--master-volume-size=16", "--ssh-public-key=~/.ssh/id_rsa.pub", "--authorization=RBAC", "--kubernetes-version=1.18.10", cni)
	err := execWithOutputAndReturnError(createCmd)
	if err != nil {
		return err
	}

	updateCmd := exec.Command("kops", "update", "cluster", clusterName, "--yes")
	return execWithOutputAndReturnError(updateCmd)
}

func WaitForClusterToBeReady(clusterName string) error {
	validateCmd := exec.Command("kops", "validate", "cluster", clusterName, "--wait", "20m")
	return execWithOutputAndReturnError(validateCmd)
}

func ExportKubecfg(clusterName string, path string) error {
	cmd := exec.Command("kops", "export", "kubecfg", "--kubeconfig", path, clusterName)
	return execWithOutputAndReturnError(cmd)
}
