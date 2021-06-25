package main

import (
	"context"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func testFlags(cleanup bool) flags {
	return flags{
		memberClusters:          []string{"member-cluster-0", "member-cluster-1", "member-cluster-2"},
		serviceAccount:          "test-service-account",
		centralCluster:          "central-cluster",
		memberClusterNamespace:  "member-namespace",
		centralClusterNamespace: "central-namespace",
		cleanup:                 cleanup,
	}
}

func TestNamespacesGetsCreatedWhenTheyDoesNotExit(t *testing.T) {
	flags := testFlags(false)
	clientMap := getClientResources(flags)
	err := ensureMultiClusterResources(flags, getFakeClientFunction(clientMap, nil))

	assert.NoError(t, err)
	assertNamespacesAreCorrect(t, clientMap, flags)
}

func TestExistingNamespacesDoNotCauseAlreadyExistsErrors(t *testing.T) {
	flags := testFlags(false)
	clientMap := getClientResources(flags, namespaceResourceType)
	err := ensureMultiClusterResources(flags, getFakeClientFunction(clientMap, nil))

	assert.NoError(t, err)
	assertNamespacesAreCorrect(t, clientMap, flags)
}

func TestServiceGetsCreatedWhenTheyDoesNotExit(t *testing.T) {
	flags := testFlags(false)
	clientMap := getClientResources(flags)
	err := ensureMultiClusterResources(flags, getFakeClientFunction(clientMap, nil))

	assert.NoError(t, err)
	assertServiceAccountsAreCorrect(t, clientMap, flags)
}

func TestExistingServiceAccountsDoNotCauseAlreadyExistsErrors(t *testing.T) {
	flags := testFlags(false)
	clientMap := getClientResources(flags, serviceAccountResourceType)
	err := ensureMultiClusterResources(flags, getFakeClientFunction(clientMap, nil))

	assert.NoError(t, err)
	assertServiceAccountsAreCorrect(t, clientMap, flags)
}

// assertNamespacesAreCorrect asserts that the correct namespaces were created the member and worker clusters.
func assertNamespacesAreCorrect(t *testing.T, clientMap map[string]*fake.Clientset, flags flags) {
	for _, clusterName := range flags.memberClusters {
		client := clientMap[clusterName]
		ns, err := client.CoreV1().Namespaces().Get(context.TODO(), flags.memberClusterNamespace, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, ns)
		assert.Equal(t, flags.memberClusterNamespace, ns.Name)
		assert.Equal(t, ns.Labels, multiClusterLabels())
	}

	client := clientMap[flags.centralCluster]
	ns, err := client.CoreV1().Namespaces().Get(context.TODO(), flags.centralClusterNamespace, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, ns)
	assert.Equal(t, flags.centralClusterNamespace, ns.Name)
	assert.Equal(t, ns.Labels, multiClusterLabels())
}

// assertServiceAccountsAreCorrect asserts the ServiceAccounts are created as expected.
func assertServiceAccountsAreCorrect(t *testing.T, clientMap map[string]*fake.Clientset, flags flags) {
	for _, clusterName := range flags.memberClusters {
		client := clientMap[clusterName]
		sa, err := client.CoreV1().ServiceAccounts(flags.memberClusterNamespace).Get(context.TODO(), flags.serviceAccount, metav1.GetOptions{})
		assert.NoError(t, err)
		assert.NotNil(t, sa)
		assert.Equal(t, flags.serviceAccount, sa.Name)
		assert.Equal(t, sa.Labels, multiClusterLabels())
	}

	client := clientMap[flags.centralCluster]
	sa, err := client.CoreV1().ServiceAccounts(flags.centralClusterNamespace).Get(context.TODO(), flags.serviceAccount, metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, sa)
	assert.Equal(t, flags.serviceAccount, sa.Name)
	assert.Equal(t, sa.Labels, multiClusterLabels())
}

// resourceType indicates a type of resource that is created during the tests.
type resourceType string

var (
	serviceAccountResourceType     resourceType = "ServiceAccount"
	namespaceResourceType          resourceType = "Namespace"
	clusterRoleBindingResourceType resourceType = "ClusterRoleBinding"
	clusterRoleResourceType        resourceType = "ClusterRole"
)

// createResourcesForCluster returns the resources specified based on the provided resourceTypes.
// this function is used to populate subsets of resources for the unit tests.
func createResourcesForCluster(centralCluster bool, flags flags, resourceTypes ...resourceType) []runtime.Object {
	var namespace = flags.memberClusterNamespace
	if centralCluster {
		namespace = flags.centralCluster
	}

	resources := make([]runtime.Object, 0)

	// always create the service account token secret as this gets created by
	// kubernetes, we can just assume it is always there for tests.
	resources = append(resources, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      flags.serviceAccount + "-token",
			Namespace: namespace,
			Labels:    multiClusterLabels(),
		},
		Data: map[string][]byte{
			"ca.crt": []byte("ca-cert-data"),
			"token":  []byte("token-data"),
		},
	})

	if containsResourceType(resourceTypes, namespaceResourceType) {
		resources = append(resources, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   namespace,
				Labels: multiClusterLabels(),
			},
		})
	}

	if containsResourceType(resourceTypes, serviceAccountResourceType) {
		resources = append(resources, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:   flags.serviceAccount,
				Labels: multiClusterLabels(),
			},
			Secrets: []corev1.ObjectReference{
				{
					Name:      flags.serviceAccount + "-token",
					Namespace: namespace,
				},
			},
		})
	}
	return resources
}

// getClientResources returns a map of cluster name to fake.Clientset
func getClientResources(flags flags, resourceTypes ...resourceType) map[string]*fake.Clientset {
	clientMap := make(map[string]*fake.Clientset)

	for _, clusterName := range flags.memberClusters {
		resources := createResourcesForCluster(false, flags, resourceTypes...)
		clientMap[clusterName] = fake.NewSimpleClientset(resources...)
	}
	resources := createResourcesForCluster(true, flags, resourceTypes...)
	clientMap[flags.centralCluster] = fake.NewSimpleClientset(resources...)

	return clientMap
}

// getFakeClientFunction returns a function which will return the fake.Clientset corresponding to the given cluster.
func getFakeClientFunction(clientResources map[string]*fake.Clientset, err error) func(clusterName, kubeConfigPath string) (kubernetes.Interface, error) {
	return func(clusterName, kubeConfigPath string) (kubernetes.Interface, error) {
		return clientResources[clusterName], err
	}
}

// containsResourceType returns true if r is in resourceTypes, otherwise false.
func containsResourceType(resourceTypes []resourceType, r resourceType) bool {
	for _, rt := range resourceTypes {
		if rt == r {
			return true
		}
	}
	return false
}
