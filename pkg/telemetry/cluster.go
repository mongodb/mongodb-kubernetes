package telemetry

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/envvar"

	corev1 "k8s.io/api/core/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	unknown   = "Unknown"
	eks       = "AWS (EKS)"
	vmware    = "VmWare"
	gke       = "Google (GKE)"
	aks       = "Azure (AKS)"
	openshift = "Openshift"
	rke       = "RKE"
	rke2      = "RKE2"
)

var kubernetesFlavourLabelsMapping = map[string]string{
	"eks.amazonaws.com/nodegroup":    eks,
	"cloud.google.com/gke-nodepool":  gke,
	"kubernetes.azure.com/agentpool": aks,
	"node.openshift.io/os_id":        openshift,
}

var kubernetesFlavourAnnotationsMapping = map[string]string{
	"rke.cattle.io/external-ip": rke,
	"rke.cattle.io/internal-ip": rke,
}

// detectClusterInfo detects the Kubernetes version and cluster flavor
func detectClusterInfos(ctx context.Context, memberClusterMap map[string]ConfigClient) []KubernetesClusterUsageSnapshotProperties {
	var clusterProperties []KubernetesClusterUsageSnapshotProperties

	for _, mgr := range memberClusterMap {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
		if err != nil {
			Logger.Debugf("failed to create discovery client: %s, sending Unknown as version", err)
		}
		clusterProperty := getKubernetesClusterProperty(ctx, discoveryClient, mgr.GetAPIReader())
		clusterProperties = append(clusterProperties, clusterProperty)
	}

	return clusterProperties
}

// getKubernetesClusterProperty returns cluster properties like:
// - kubernetes server version
// - cloud provider (openshift, eks ...)
// - kubernetes cluster uid
// Notes: We are using a non-cached client to ensure we are properly timing out in case we don't have the
// necessary RBACs.
func getKubernetesClusterProperty(ctx context.Context, discoveryClient discovery.DiscoveryInterface, uncachedClient kubeclient.Reader) KubernetesClusterUsageSnapshotProperties {
	kubernetesAPIVersion := unknown
	kubeClusterUUID := getKubernetesClusterUUID(ctx, uncachedClient)

	if discoveryClient != nil {
		if versionInfo := getServerVersion(discoveryClient); versionInfo != nil {
			kubernetesAPIVersion = versionInfo.GitVersion
		}
	}

	kubernetesFlavour := detectKubernetesFlavour(ctx, uncachedClient, kubernetesAPIVersion)

	property := KubernetesClusterUsageSnapshotProperties{
		KubernetesClusterID:  kubeClusterUUID,
		KubernetesFlavour:    kubernetesFlavour,
		KubernetesAPIVersion: kubernetesAPIVersion,
	}

	return property
}

func getServerVersion(discoveryClient discovery.DiscoveryInterface) *version.Info {
	versionInfo, err := discoveryClient.ServerVersion()
	if err != nil {
		Logger.Debugf("Failed to fetch server version: %s", err)
		return nil
	}
	return versionInfo
}

// detectKubernetesFlavour detects the cloud provider based on node labels.
func detectKubernetesFlavour(ctx context.Context, uncachedClient kubeclient.Reader, kubeGitApiVersion string) string {
	// Check Kubernetes API version for known cloud providers
	switch {
	case strings.Contains(kubeGitApiVersion, "+rke2"):
		return rke2
	case strings.Contains(kubeGitApiVersion, "-gke"):
		return gke
	case strings.Contains(kubeGitApiVersion, "-eks"):
		return eks
	case strings.Contains(kubeGitApiVersion, "+vmware"):
		return vmware
	}

	// Limit is propagated to the apiserver which propagates to etcd as it is. Thus, there is not a lot of
	// work required on the APIServer and ETCD to retrieve that node even in large clusters
	nodes := &corev1.NodeList{}
	if err := uncachedClient.List(ctx, nodes, &kubeclient.ListOptions{Limit: 1}); err != nil {
		Logger.Debugf("Failed to fetch node to detect the cloud provider: %s", err)
		return unknown
	}
	if len(nodes.Items) == 0 {
		Logger.Debugf("No nodes found, returning Unknown")
		return unknown
	}

	node := nodes.Items[0]
	labels, annotations := node.Labels, node.Annotations

	for key, provider := range kubernetesFlavourLabelsMapping {
		if _, exists := labels[key]; exists {
			return provider
		}
	}

	for key, provider := range kubernetesFlavourAnnotationsMapping {
		if _, exists := annotations[key]; exists {
			return provider
		}
	}

	return unknown
}

// getKubernetesClusterUUID retrieves the UUID from the kube-system namespace.
// We are using a non-cached client to ensure we are properly timing out in case we don't have the
// necessary RBACs.
func getKubernetesClusterUUID(ctx context.Context, uncachedClient kubeclient.Reader) string {
	timeoutLengthStr := envvar.GetEnvOrDefault(KubeTimeout, "5m")
	duration, err := time.ParseDuration(timeoutLengthStr)
	if err != nil {
		Logger.Warnf("Failed converting %s to a duration, using default 5m", KubeTimeout)
		duration = 5 * time.Minute
	}
	nonCachedClientTimeout, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	namespace := &corev1.Namespace{}
	err = uncachedClient.Get(nonCachedClientTimeout, kubeclient.ObjectKey{Name: "kube-system"}, namespace)
	if err != nil {
		Logger.Debugf("failed to fetch kube-system namespace: %s", err)
		return unknown
	}

	return string(namespace.ObjectMeta.UID)
}
