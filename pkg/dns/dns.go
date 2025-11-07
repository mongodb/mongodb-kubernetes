package dns

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

func GetMultiPodName(stsName string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s-%d-%d", stsName, clusterNum, podNum)
}

func GetMultiServiceName(stsName string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s-svc", GetMultiPodName(stsName, clusterNum, podNum))
}

func GetMultiHeadlessServiceName(stsName string, clusterNum int) string {
	return fmt.Sprintf("%s-%d-svc", stsName, clusterNum)
}

func GetServiceName(stsName string) string {
	return fmt.Sprintf("%s-svc", stsName)
}

func GetExternalServiceName(stsName string, podNum int) string {
	return fmt.Sprintf("%s-%d-svc-external", stsName, podNum)
}

func GetMultiExternalServiceName(stsName string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s-external", GetMultiServiceName(stsName, clusterNum, podNum))
}

func GetMultiServiceFQDN(stsName string, namespace string, clusterNum int, podNum int, clusterDomain string) string {
	domain := "cluster.local"
	if len(clusterDomain) > 0 {
		domain = strings.TrimPrefix(clusterDomain, ".")
	}

	return fmt.Sprintf("%s.%s.svc.%s", GetMultiServiceName(stsName, clusterNum, podNum), namespace, domain)
}

func GetMultiServiceExternalDomain(stsName, externalDomain string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s.%s", GetMultiPodName(stsName, clusterNum, podNum), externalDomain)
}

// GetMultiClusterProcessHostnames returns the agent hostnames, which they should be registered in OM in multi-cluster mode.
func GetMultiClusterProcessHostnames(stsName, namespace string, clusterNum, members int, clusterDomain string, externalDomain *string) []string {
	hostnames, _ := GetMultiClusterProcessHostnamesAndPodNames(stsName, namespace, clusterNum, members, clusterDomain, externalDomain)
	return hostnames
}

func GetMultiClusterProcessHostnamesAndPodNames(stsName, namespace string, clusterNum, members int, clusterDomain string, externalDomain *string) ([]string, []string) {
	hostnames := make([]string, 0)
	podNames := make([]string, 0)

	for podNum := 0; podNum < members; podNum++ {
		hostnames = append(hostnames, GetMultiClusterPodServiceFQDN(stsName, namespace, clusterNum, externalDomain, podNum, clusterDomain))
		podNames = append(podNames, GetMultiPodName(stsName, clusterNum, podNum))
	}

	return hostnames, podNames
}

func GetMultiClusterPodServiceFQDN(stsName string, namespace string, clusterNum int, externalDomain *string, podNum int, clusterDomain string) string {
	if externalDomain != nil {
		return GetMultiServiceExternalDomain(stsName, *externalDomain, clusterNum, podNum)
	}
	return GetMultiServiceFQDN(stsName, namespace, clusterNum, podNum, clusterDomain)
}

func GetServiceDomain(namespace string, clusterDomain string, externalDomain *string) string {
	if externalDomain != nil {
		return *externalDomain
	}
	if clusterDomain == "" {
		clusterDomain = "cluster.local"
	}
	return fmt.Sprintf("%s.svc.%s", namespace, clusterDomain)
}

// GetDnsForStatefulSet returns hostnames and names of pods in stateful set "set". This is a preferred way of getting hostnames
// it must be always used if it's possible to read the statefulset from Kubernetes
func GetDnsForStatefulSet(set appsv1.StatefulSet, clusterDomain string, externalDomain *string) ([]string, []string) {
	return GetDnsForStatefulSetReplicasSpecified(set, clusterDomain, 0, externalDomain)
}

// GetDnsForStatefulSetReplicasSpecified is similar to GetDnsForStatefulSet but expects the number of replicas to be specified
// (important for scale-down operations to support hostnames for old statefulset)
func GetDnsForStatefulSetReplicasSpecified(set appsv1.StatefulSet, clusterDomain string, replicas int, externalDomain *string) ([]string, []string) {
	if replicas == 0 {
		replicas = int(*set.Spec.Replicas)
	}
	return GetDNSNames(set.Name, set.Spec.ServiceName, set.Namespace, clusterDomain, replicas, externalDomain)
}

// GetDNSNames returns hostnames and names of pods in stateful set, it's less preferable than "GetDnsForStatefulSet" and
// should be used only in situations when statefulset doesn't exist any more (the main example is when the mongodb custom
// resource is being deleted - then the dependant statefulsets cannot be read any more as they get into Terminated state)
func GetDNSNames(statefulSetName, service, namespace, clusterDomain string, replicas int, externalDomain *string) (hostnames, names []string) {
	names = make([]string, replicas)
	hostnames = make([]string, replicas)

	for i := 0; i < replicas; i++ {
		names[i] = GetPodName(statefulSetName, i)
		hostnames[i] = GetPodFQDN(names[i], service, namespace, clusterDomain, externalDomain)
	}
	return hostnames, names
}

func GetPodFQDN(podName string, serviceName string, namespace string, clusterDomain string, externalDomain *string) string {
	if externalDomain != nil && len(*externalDomain) > 0 {
		return fmt.Sprintf("%s.%s", podName, *externalDomain)
	} else {
		return fmt.Sprintf("%s.%s", podName, GetServiceFQDN(serviceName, namespace, clusterDomain))
	}
}

// GetServiceFQDN returns the FQDN for the service inside Kubernetes
func GetServiceFQDN(serviceName string, namespace string, clusterDomain string) string {
	return fmt.Sprintf("%s.%s", serviceName, GetServiceDomain(namespace, clusterDomain, nil))
}

func GetPodName(stsName string, idx int) string {
	return fmt.Sprintf("%s-%d", stsName, idx)
}

func GetMultiStatefulSetName(replicaSetName string, clusterNum int) string {
	return fmt.Sprintf("%s-%d", replicaSetName, clusterNum)
}
