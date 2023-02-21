package dns

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

func GetMultiPodName(mdbmName string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s-%d-%d", mdbmName, clusterNum, podNum)
}

func GetServiceName(mdbmName string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s-%d-%d-svc", mdbmName, clusterNum, podNum)
}

func GetMultiServiceFQDN(mdbmName, namespace string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", GetServiceName(mdbmName, clusterNum, podNum), namespace)
}

func GetMultiServiceExternalDomain(mdbmName, externalDomain string, clusterNum, podNum int) string {
	return fmt.Sprintf("%s.%s", GetMultiPodName(mdbmName, clusterNum, podNum), externalDomain)
}

// GetMultiClusterAgentHostnames returns the agent hostnames, which they should be registered in OM in multi-cluster mode.
func GetMultiClusterAgentHostnames(mdbmName, namespace string, clusterNum, members int, externalDomain *string) []string {
	hostnames := make([]string, 0)

	for podNum := 0; podNum < members; podNum++ {
		var hostname string
		if externalDomain != nil {
			hostname = GetMultiServiceExternalDomain(mdbmName, *externalDomain, clusterNum, podNum)
		} else {
			hostname = GetMultiServiceFQDN(mdbmName, namespace, clusterNum, podNum)
		}
		hostnames = append(hostnames, hostname)
	}

	return hostnames
}

// GetDnsForStatefulSet returns hostnames and names of pods in stateful set "set". This is a preferred way of getting hostnames
// it must be always used if it's possible to read the statefulset from Kubernetes
func GetDnsForStatefulSet(set appsv1.StatefulSet, clusterName string, externalDomain *string) ([]string, []string) {
	return GetDnsForStatefulSetReplicasSpecified(set, clusterName, 0, externalDomain)
}

// GetDnsForStatefulSetReplicasSpecified is similar to GetDnsForStatefulSet but expects the number of replicas to be specified
// (important for scale-down operations to support hostnames for old statefulset)
func GetDnsForStatefulSetReplicasSpecified(set appsv1.StatefulSet, clusterName string, replicas int, externalDomain *string) ([]string, []string) {
	if replicas == 0 {
		replicas = int(*set.Spec.Replicas)
	}
	return GetDNSNames(set.Name, set.Spec.ServiceName, set.Namespace, clusterName, replicas, externalDomain)
}

// GetDnsNames returns hostnames and names of pods in stateful set, it's less preferable than "GetDnsForStatefulSet" and
// should be used only in situations when statefulset doesn't exist any more (the main example is when the mongodb custom
// resource is being deleted - then the dependant statefulsets cannot be read any more as they get into Terminated state)
func GetDNSNames(statefulSetName, service, namespace, clusterName string, replicas int, externalDomain *string) (hostnames, names []string) {
	names = make([]string, replicas)
	hostnames = make([]string, replicas)

	if externalDomain != nil && len(*externalDomain) > 0 {
		for i := 0; i < replicas; i++ {
			names[i] = GetPodName(statefulSetName, i)
			hostnames[i] = fmt.Sprintf("%s.%s", names[i], *externalDomain)
		}
	} else {
		mName := getDnsTemplateFor(statefulSetName, service, namespace, clusterName)

		for i := 0; i < replicas; i++ {
			hostnames[i] = fmt.Sprintf(mName, i)
			names[i] = GetPodName(statefulSetName, i)
		}
	}

	return hostnames, names
}

// GetServiceFQDN returns the FQDN for the service inside Kubernetes
func GetServiceFQDN(service, namespace, clusterDomain string) string {
	if clusterDomain == "" {
		clusterDomain = "cluster.local"
	}
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, clusterDomain)
}

// getDnsTemplateFor returns a template-FQDN for a StatefulSet. This
// name will lack one parameter: the index for a given Pod, so the form of
// the returned fqdn will be:
//
// <name>-%d.<service>.<namespace>.svc.<cluster>
//
// The calling code is responsible for interpolating the right index when
// necessary.
//
// TODO: The cluster domain is not known inside the Kubernetes cluster,
// so there is no API to obtain this name from the operator.
// * See: https://github.com/kubernetes/kubernetes/issues/44954
func getDnsTemplateFor(name, service, namespace, clusterDomain string) string {
	if clusterDomain == "" {
		clusterDomain = "cluster.local"
	}
	dnsTemplate := fmt.Sprintf("%s-{}.%s.%s.svc.%s", name, service, namespace, clusterDomain)
	return strings.Replace(dnsTemplate, "{}", "%d", 1)
}

func GetPodName(name string, idx int) string {
	return fmt.Sprintf("%s-%d", name, idx)
}
