package multicluster

import (
	"fmt"
	"io/ioutil"
	"strconv"
	"strings"

	"github.com/ghodss/yaml"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// kubeconfig path holding the credentials for different member clusters
	kubeConfigPath = "/etc/config/kubeconfig/kubeconfig"
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
		clientset, err := getClient(c, kubeConfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create clientset map: %s", err)
		}
		clusterClientsMap[c] = clientset
	}
	return clusterClientsMap, nil
}

// LoadKubeConfigFile returns the KubeConfig file containing the multi cluster context.
func LoadKubeConfigFile() (KubeConfigFile, error) {
	kubeConfigBytes, err := ioutil.ReadFile(kubeConfigPath)
	if err != nil {
		return KubeConfigFile{}, err
	}

	kubeConfig := KubeConfigFile{}
	if err := yaml.Unmarshal(kubeConfigBytes, &kubeConfig); err != nil {
		return KubeConfigFile{}, err
	}
	return kubeConfig, nil
}

// IsMultiClusterMode checks if the operator is running in multi-cluster mode.
// In multi-cluster mode the operator is passsed the name of the CRD in command line arguments.
func IsMultiClusterMode(crdsToWatch string) bool {
	return strings.Contains(crdsToWatch, "mongodbmulti")
}

// KubeConfigFile represents the contents of a KubeConfig file.
type KubeConfigFile struct {
	Contexts []KubeConfigContextItem `json:"contexts"`
}
type KubeConfigContextItem struct {
	Name    string            `json:"name"`
	Context KubeConfigContext `json:"context"`
}

type KubeConfigContext struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
}

// GetMemberClusterNamespace returns the namespace that will be used for all member clusters.
func (k KubeConfigFile) GetMemberClusterNamespace() string {
	return k.Contexts[0].Context.Namespace
}

// MustGetClusterNumFromMultiStsName parses the statefulset object name and returns the cluster number where it is created
func MustGetClusterNumFromMultiStsName(name string) int {
	ss := strings.Split(name, "-")

	n, err := strconv.Atoi(ss[len(ss)-1])
	if err != nil {
		panic(err)
	}
	return n
}

// GetRsNamefromMultiStsName parese the statefulset object name and returns the name of MongoDBMulti object name
func GetRsNamefromMultiStsName(name string) string {
	ss := strings.Split(name, "-")
	if len(ss) <= 1 || ss[0] == "" {
		panic(fmt.Sprintf("invalid statefulset name: %s", name))
	}
	return strings.Join(ss[:len(ss)-1], "-")
}
