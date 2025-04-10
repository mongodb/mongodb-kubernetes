package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fake2 "k8s.io/client-go/discovery/fake"
	clientFake "k8s.io/client-go/kubernetes/fake"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// TestGetKubernetesClusterProperty - Unit tests for getKubernetesClusterProperty
func TestGetKubernetesClusterProperty(t *testing.T) {
	ctx := context.Background()

	// Define test cases
	tests := []struct {
		name               string
		discoveryClient    discovery.DiscoveryInterface
		uncachedClient     kubeclient.Reader
		expectedClusterID  string
		expectedAPIVersion string
		expectedFlavour    string
	}{
		{
			name: "Valid EKS Cluster",
			discoveryClient: func() *fake2.FakeDiscovery {
				fakeDiscovery := clientFake.NewSimpleClientset().Discovery().(*fake2.FakeDiscovery)
				fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v1.21.0"}
				return fakeDiscovery
			}(),
			uncachedClient: fake.NewFakeClient(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						UID:  "mock-cluster-uuid",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"eks.amazonaws.com/nodegroup": "default",
						},
					},
				},
			),
			expectedClusterID:  "mock-cluster-uuid",
			expectedAPIVersion: "v1.21.0",
			expectedFlavour:    eks,
		},
		{
			name:            "Missing Discovery Client",
			discoveryClient: nil,
			uncachedClient: fake.NewFakeClient(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						UID:  "mock-cluster-uuid",
					},
				},
			),
			expectedClusterID:  "mock-cluster-uuid",
			expectedAPIVersion: unknown, // Should default to "unknown"
			expectedFlavour:    unknown, // No node labels, should be unknown
		},
		{
			name: "GKE Cluster",
			discoveryClient: func() *fake2.FakeDiscovery {
				fakeDiscovery := clientFake.NewSimpleClientset().Discovery().(*fake2.FakeDiscovery)
				fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v1.24.3"}
				return fakeDiscovery
			}(),
			uncachedClient: fake.NewFakeClient(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						UID:  "mock-cluster-uuid-gke",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"cloud.google.com/gke-nodepool": "default-pool",
						},
					},
				},
			),
			expectedClusterID:  "mock-cluster-uuid-gke",
			expectedAPIVersion: "v1.24.3",
			expectedFlavour:    gke,
		},
		{
			name: "AKS Cluster",
			discoveryClient: func() *fake2.FakeDiscovery {
				fakeDiscovery := clientFake.NewSimpleClientset().Discovery().(*fake2.FakeDiscovery)
				fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v1.24.3"}
				return fakeDiscovery
			}(),
			uncachedClient: fake.NewFakeClient(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						UID:  "mock-cluster-uuid-aks",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"kubernetes.azure.com/agentpool": "default-pool",
						},
					},
				},
			),
			expectedClusterID:  "mock-cluster-uuid-aks",
			expectedAPIVersion: "v1.24.3",
			expectedFlavour:    aks,
		},
		{
			name: "OpenShift Cluster",
			discoveryClient: func() *fake2.FakeDiscovery {
				fakeDiscovery := clientFake.NewSimpleClientset().Discovery().(*fake2.FakeDiscovery)
				fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v4.9.0"}
				return fakeDiscovery
			}(),
			uncachedClient: fake.NewFakeClient(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
						UID:  "mock-cluster-uuid-openshift",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"node.openshift.io/os_id": "rhcos",
						},
					},
				},
			),
			expectedClusterID:  "mock-cluster-uuid-openshift",
			expectedAPIVersion: "v4.9.0",
			expectedFlavour:    openshift,
		},
	}

	// Run test cases
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			property := getKubernetesClusterProperty(ctx, tt.discoveryClient, tt.uncachedClient)

			assert.Equal(t, tt.expectedClusterID, property.KubernetesClusterID, "Cluster ID mismatch")
			assert.Equal(t, tt.expectedAPIVersion, property.KubernetesAPIVersion, "API version mismatch")
			assert.Equal(t, tt.expectedFlavour, property.KubernetesFlavour, "Kubernetes flavour mismatch")
		})
	}
}

func TestDetectKubernetesFlavour(t *testing.T) {
	ctx := context.Background()

	// Define test cases
	tests := []struct {
		name            string
		labels          map[string]string
		gitVersion      string
		expectedFlavour string
	}{
		{
			name:            "eks",
			labels:          map[string]string{"eks.amazonaws.com/nodegroup": "default", "eks.amazonaws.com/cluster": "default"},
			expectedFlavour: eks,
		},
		{
			name:            "unknown",
			labels:          map[string]string{"something": "default"},
			expectedFlavour: unknown,
		},
		{
			name:            "based on gitversion",
			labels:          map[string]string{"something": "default"},
			gitVersion:      "v123-gke",
			expectedFlavour: gke,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					Labels:    tt.labels,
				},
			}

			fakeClient := fake.NewClientBuilder().WithObjects(node).Build()
			cloudProvider := detectKubernetesFlavour(ctx, fakeClient, tt.gitVersion)

			assert.Equal(t, tt.expectedFlavour, cloudProvider)
		})
	}
}

func TestGetKubernetesClusterUUID(t *testing.T) {
	ctx := context.Background()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kube-system",
			UID:  "fake-uuid",
		},
	}

	fakeClient := fake.NewClientBuilder().WithObjects(namespace).Build()
	uuid := getKubernetesClusterUUID(ctx, fakeClient)

	assert.Equal(t, "fake-uuid", uuid)
}
