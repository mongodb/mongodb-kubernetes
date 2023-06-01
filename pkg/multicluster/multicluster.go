package multicluster

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/ghodss/yaml"
	"golang.org/x/xerrors"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// kubeconfig path holding the credentials for different member clusters
	DefaultKubeConfigPath   = "/etc/config/kubeconfig/kubeconfig"
	KubeConfigPathEnv       = "KUBE_CONFIG_PATH"
	ClusterClientTimeoutEnv = "CLUSTER_CLIENT_TIMEOUT"
)

type KubeConfig struct {
	Reader io.Reader
}

func NewKubeConfigFile() (KubeConfig, error) {
	file, err := os.Open(GetKubeConfigPath())
	if err != nil {
		return KubeConfig{}, err
	}
	return KubeConfig{Reader: file}, nil
}

func GetKubeConfigPath() string {
	return env.ReadOrDefault(KubeConfigPathEnv, DefaultKubeConfigPath)
}

// LoadKubeConfigFile returns the KubeConfig file containing the multi cluster context.
func (k KubeConfig) LoadKubeConfigFile() (KubeConfigFile, error) {
	kubeConfigBytes, err := ioutil.ReadAll(k.Reader)
	if err != nil {
		return KubeConfigFile{}, err
	}

	kubeConfig := KubeConfigFile{}
	if err := yaml.Unmarshal(kubeConfigBytes, &kubeConfig); err != nil {
		return KubeConfigFile{}, err
	}
	return kubeConfig, nil
}

// CreateMemberClusterClients creates a client(map of cluster-name to client) to talk to the API-Server corresponding to each member clusters.
func CreateMemberClusterClients(clusterNames []string) (map[string]*restclient.Config, error) {
	clusterClientsMap := map[string]*restclient.Config{}

	for _, c := range clusterNames {
		clientset, err := getClient(c, GetKubeConfigPath())
		if err != nil {
			return nil, xerrors.Errorf("failed to create clientset map: %w", err)
		}
		if clientset == nil {
			return nil, xerrors.Errorf("failed to get clientset for cluster: %s", c)
		}
		clientset.Timeout = time.Duration(env.ReadIntOrDefault(ClusterClientTimeoutEnv, 10)) * time.Second
		clusterClientsMap[c] = clientset
	}
	return clusterClientsMap, nil
}

// getClient returns a kubernetes.Clientset using the given context from the
// specified KubeConfig filepath.
func getClient(context, kubeConfigPath string) (*restclient.Config, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()

	if err != nil {
		return nil, xerrors.Errorf("failed to create client config: %w", err)
	}

	return config, nil
}

// IsMultiClusterMode checks if the operator is running in multi-cluster mode.
// In multi-cluster mode the operator is passed the name of the CRD in command line arguments.
func IsMultiClusterMode(crdsToWatch string) bool {
	return strings.Contains(crdsToWatch, "mongodbmulticluster")
}

// shouldPerformFailover checks if the operator is configured to perform automatic failover
// of the MongoDB Replicaset members spread over multiple Kubernetes clusters.
func ShouldPerformFailover() bool {
	str := os.Getenv("PERFORM_FAILOVER")
	val, err := strconv.ParseBool(str)
	if err != nil {
		return false
	}
	return val
}

// KubeConfigFile represents the contents of a KubeConfig file.
type KubeConfigFile struct {
	Contexts []KubeConfigContextItem `json:"contexts"`
	Clusters []KubeConfigClusterItem `json:"clusters"`
	Users    []KubeConfigUserItem    `json:"users"`
}

type KubeConfigClusterItem struct {
	Cluster KubeConfigCluster `json:"cluster"`
}

type KubeConfigCluster struct {
	CertificateAuthority string `json:"certificate-authority-data"`
	Server               string `json:"server"`
}

type KubeConfigUserItem struct {
	User KubeConfigUser `json:"user"`
}

type KubeConfigUser struct {
	Token string `json:"token"`
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

// GetRsNamefromMultiStsName parese the statefulset object name and returns the name of MongoDBMultiCluster object name
func GetRsNamefromMultiStsName(name string) string {
	ss := strings.Split(name, "-")
	if len(ss) <= 1 || ss[0] == "" {
		panic(fmt.Sprintf("invalid statefulset name: %s", name))
	}
	return strings.Join(ss[:len(ss)-1], "-")
}
