package operator

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

func GetDnsForStatefulSet(set *appsv1.StatefulSet, clusterName string) []string {
	mName := getDnsTemplateFor(set.Name, set.Spec.ServiceName, set.Namespace, clusterName)
	replicas := int(*set.Spec.Replicas)
	names := make([]string, replicas)

	for i := 0; i < replicas; i++ {
		names[i] = fmt.Sprintf(mName, i)
	}

	return names
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
func getDnsTemplateFor(name, service, namespace, cluster string) string {
	if cluster == "" {
		cluster = "cluster.local"
	}
	dnsTemplate := fmt.Sprintf("%s-{}.%s.%s.svc.%s", name, service, namespace, cluster)
	return strings.Replace(dnsTemplate, "{}", "%d", 1)
}

func GetDnsNameFor(name, service, namespace, cluster string, idx int) string {
	return fmt.Sprintf(getDnsTemplateFor(name, service, namespace, cluster), idx)
}

func GetPodName(name string, idx int) string {
	return fmt.Sprintf("%s-%d", name, idx)
}
