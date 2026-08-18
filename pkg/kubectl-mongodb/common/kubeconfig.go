package common

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/xerrors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	kubeConfigEnv = "KUBECONFIG"
)

// LoadKubeConfigFilePath returns the path of the local KubeConfig file.
func LoadKubeConfigFilePath() string {
	env := os.Getenv(kubeConfigEnv) // nolint:forbidigo
	if env != "" {
		return env
	}
	return filepath.Join(homedir.HomeDir(), ".kube", "config")
}

// GetMemberClusterApiServerUrls returns the slice of member cluster api urls that should be used.
func GetMemberClusterApiServerUrls(kubeconfig *clientcmdapi.Config, clusterNames []string) ([]string, error) {
	var urls []string
	for _, name := range clusterNames {
		if cluster := kubeconfig.Clusters[name]; cluster != nil {
			urls = append(urls, cluster.Server)
		} else {
			return nil, xerrors.Errorf("cluster '%s' not found in kubeconfig", name)
		}
	}
	return urls, nil
}

// ParseMemberClusterCAs parses the repeated --member-cluster-ca entries, each of the form
// <memberClusterName>=<pathToPemFile>, and returns a map of member cluster name to the contents of
// the referenced PEM file. Every file is read and validated here, before any cluster is modified,
// so that a bad entry fails the command instead of leaving half created resources behind.
func ParseMemberClusterCAs(entries []string, memberClusters []string) (map[string][]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	cas := map[string][]byte{}
	for _, entry := range entries {
		clusterName, path, found := strings.Cut(entry, "=")
		clusterName = strings.TrimSpace(clusterName)
		path = strings.TrimSpace(path)
		if !found || clusterName == "" || path == "" {
			return nil, xerrors.Errorf("invalid member-cluster-ca value '%s', expected format <memberClusterName>=<pathToPemFile>", entry)
		}

		if !slices.Contains(memberClusters, clusterName) {
			return nil, xerrors.Errorf("member-cluster-ca refers to cluster '%s' which is not one of the member clusters %v", clusterName, memberClusters)
		}

		if _, ok := cas[clusterName]; ok {
			return nil, xerrors.Errorf("member-cluster-ca specified more than once for cluster '%s'", clusterName)
		}

		caPEM, err := os.ReadFile(path)
		if err != nil {
			return nil, xerrors.Errorf("failed reading CA file '%s' for cluster '%s': %w", path, clusterName, err)
		}

		// The Operator's member cluster health checker builds a cert pool from this value alone,
		// with no fallback to the system trust store, so an unusable bundle would silently mark
		// every member cluster unhealthy. Reject it here instead.
		if !x509.NewCertPool().AppendCertsFromPEM(caPEM) {
			return nil, xerrors.Errorf("CA file '%s' for cluster '%s' does not contain any PEM encoded certificate", path, clusterName)
		}

		cas[clusterName] = caPEM
	}

	return cas, nil
}

// CreateClientMap crates a map of all MultiClusterClient for every member cluster, and the operator cluster.
func CreateClientMap(memberClusters []string, operatorCluster, kubeConfigPath string, getClient func(clusterName string, kubeConfigPath string) (KubeClient, error)) (map[string]KubeClient, error) {
	clientMap := map[string]KubeClient{}
	for _, c := range memberClusters {
		clientset, err := getClient(c, kubeConfigPath)
		if err != nil {
			return nil, xerrors.Errorf("failed to create clientset map: %w", err)
		}
		clientMap[c] = clientset
	}

	clientset, err := getClient(operatorCluster, kubeConfigPath)
	if err != nil {
		return nil, xerrors.Errorf("failed to create clientset map: %w", err)
	}
	clientMap[operatorCluster] = clientset
	return clientMap, nil
}

// GetKubernetesClient returns a kubernetes.Clientset using the given context from the
// specified KubeConfig filepath.
func GetKubernetesClient(context, kubeConfigPath string) (KubeClient, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()
	if err != nil {
		return nil, xerrors.Errorf("failed to create client config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, xerrors.Errorf("failed to create kubernetes clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, xerrors.Errorf("failed to create dynamic kubernetes clientset: %w", err)
	}

	return NewKubeClientContainer(config, clientset, dynamicClient), nil
}
