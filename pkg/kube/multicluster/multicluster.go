package multicluster

import (
	"fmt"
	"strings"

	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// getClient returns a kubernetes.Clientset using the given context from the
// specified KubeConfig filepath.
func getClient(context, kubeConfigPath string) (*restclient.Config, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()

	if err != nil {
		return nil, fmt.Errorf("failed to create client config: %s", err)
	}

	return config, nil
}

// CreateMemberClusterClients creates a client(map of cluster-name to client) to talk to the API-Server corresponding to each member clusters.
func CreateMemberClusterClients(clusterNames []string) (map[string]*restclient.Config, error) {
	clusterClientsMap := map[string]*restclient.Config{}

	for _, c := range clusterNames {
		clientset, err := getClient(c, "")
		if err != nil {
			return nil, fmt.Errorf("failed to create clientset map: %s", err)
		}
		clusterClientsMap[c] = clientset
	}
	return clusterClientsMap, nil
}

// IsMultiClusterMode checks if the operator is running in multi-cluster mode.
// In multi-cluster mode the operator is passsed the name of the CRD in command line arguments.
func IsMultiClusterMode(crdsToWatch string) bool {
	return strings.Contains(crdsToWatch, "mongodbmulti")
}
