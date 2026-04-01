// Integration test for the per-pod-secret mutating admission webhook.
// Requires a local kind cluster (not a remote cluster — the webhook server runs on the host machine).
//
// To run:
//
//	kind create cluster --name webhook-poc
//	KUBECONFIG=~/.kube/config go test -v -run TestAdmissionWebhookIntegration ./poc/admission-webhook/...
//	kind delete cluster --name webhook-poc
package admissionwebhook_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionwebhook "github.com/mongodb/mongodb-kubernetes/poc/admission-webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	testNamespace = "default"
	webhookName   = "per-pod-secret.mongodb.com"
	stsName       = "my-sts"
)

func TestAdmissionWebhookIntegration(t *testing.T) {
	// Build kube client — requires KUBECONFIG env var or ~/.kube/config
	clientset := newClientset(t)

	// 1. Detect host IP: the address the kube API server can reach this process on
	hostIP := detectHostIP(t, clientset)
	t.Logf("using host IP: %s", hostIP)

	// 2. Start webhook HTTPS server; cert SAN includes the detected host IP
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-pods", admissionwebhook.HandleMutatePods)
	certPEM, boundAddr, err := admissionwebhook.Start(":0", []string{hostIP}, mux)
	require.NoError(t, err)
	_, portStr, err := net.SplitHostPort(boundAddr)
	require.NoError(t, err)
	webhookURL := fmt.Sprintf("https://%s:%s/mutate-pods", hostIP, portStr)
	t.Logf("webhook URL: %s", webhookURL)

	// 3. Register MutatingWebhookConfiguration; cleanup registered IMMEDIATELY (LIFO: runs last)
	registerWebhook(t, clientset, webhookURL, certPEM)

	// Wait for the API server to pick up the new webhook configuration before applying the StatefulSet.
	// Without this, pods may be created and admitted before the webhook is active.
	t.Log("waiting 5s for webhook config propagation...")
	time.Sleep(5 * time.Second)

	// 4. Create per-pod Secrets (cleanup registered after each creation; LIFO: run before webhook cleanup)
	secretValues := map[string]string{
		"my-sts-0": "cert-data-for-pod-my-sts-0",
		"my-sts-1": "cert-data-for-pod-my-sts-1",
		"my-sts-2": "cert-data-for-pod-my-sts-2",
	}
	for name, value := range secretValues {
		createSecret(t, clientset, name, value)
	}

	// 5. Apply StatefulSet from testdata/sts.yaml
	// NOTE: go test sets the working directory to the package directory (poc/admission-webhook/)
	// so the relative path "testdata/sts.yaml" is correct when running via `go test ./poc/admission-webhook/...`
	stsYAML := filepath.Join("testdata", "sts.yaml")
	applyYAML(t, stsYAML)
	t.Cleanup(func() { deleteYAML(stsYAML) })

	// 6. Wait for all 3 pods to reach Running phase (2 min timeout, owned by waitForPodsRunning)
	waitForPodsRunning(t, clientset, stsName, 3)

	// 7. Assert each pod spec has the correct volume mount injected by the webhook
	for ordinal := 0; ordinal < 3; ordinal++ {
		podName := fmt.Sprintf("%s-%d", stsName, ordinal)
		expectedSecret := fmt.Sprintf("%s-%d", stsName, ordinal)
		assertVolumeMount(t, clientset, podName, expectedSecret)
	}

	// 8. Poll pod logs until the expected cert content appears (60s per pod, independent timeouts)
	for ordinal := 0; ordinal < 3; ordinal++ {
		podName := fmt.Sprintf("%s-%d", stsName, ordinal)
		waitForPodLogs(t, clientset, podName, secretValues[podName])
	}
}

// newClientset builds a *kubernetes.Clientset from the default kubeconfig loading rules.
// Respects the KUBECONFIG env var and falls back to ~/.kube/config.
// Run `source .generated/context.export.env` before executing the test.
func newClientset(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	require.NoError(t, err, "loading kubeconfig — did you source .generated/context.export.env?")
	clientset, err := kubernetes.NewForConfig(restCfg)
	require.NoError(t, err)
	return clientset
}

// detectHostIP returns the IP address of this machine as seen from the cluster.
//
// Strategy: dial the kube API server on the node's InternalIP:6443.
//   - On Linux/native kind: the node IP is routable from the host → return nodeIP.
//   - On macOS/Docker Desktop: the node IP is a container-internal address and port 6443
//     is not reachable from the macOS host → fall back to "host.docker.internal".
//
// Caveat: assumes kube API server is on port 6443 (true for kind; may differ for other distros).
func detectHostIP(t *testing.T, clientset *kubernetes.Clientset) string {
	t.Helper()
	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, nodes.Items, "no nodes found in cluster")

	var nodeIP string
	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			nodeIP = addr.Address
			break
		}
	}
	require.NotEmpty(t, nodeIP, "no InternalIP found on cluster node")

	conn, err := net.DialTimeout("tcp", nodeIP+":6443", 2*time.Second)
	if err == nil {
		conn.Close()
		t.Logf("detectHostIP: node IP %s is reachable — using it (Linux/native kind)", nodeIP)
		return nodeIP
	}
	t.Logf("detectHostIP: node IP %s unreachable — falling back to host.docker.internal (macOS/Docker Desktop)", nodeIP)
	return "host.docker.internal"
}

// registerWebhook creates the MutatingWebhookConfiguration and registers its cleanup immediately.
// t.Cleanup runs in LIFO order, so registering cleanup here (before secrets and STS) means
// the webhook config is deleted last — after the StatefulSet and Secrets are gone.
func registerWebhook(t *testing.T, clientset *kubernetes.Clientset, webhookURL string, caBundle []byte) {
	t.Helper()
	failurePolicy := admissionregistrationv1.Ignore
	sideEffects := admissionregistrationv1.SideEffectClassNone
	scope := admissionregistrationv1.NamespacedScope

	cfg := admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: webhookName},
		Webhooks: []admissionregistrationv1.MutatingWebhook{
			{
				Name: webhookName,
				ClientConfig: admissionregistrationv1.WebhookClientConfig{
					URL:      &webhookURL,
					CABundle: caBundle,
				},
				Rules: []admissionregistrationv1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
						Rule: admissionregistrationv1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
							Scope:       &scope,
						},
					},
				},
				ObjectSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"per-pod-secret-webhook/enabled": "true"},
				},
				// Restrict to test namespace to limit blast radius
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": testNamespace},
				},
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy, // Ignore: safe for PoC
			},
		},
	}

	_, err := clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(
		context.Background(), &cfg, metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// LIFO cleanup: registered first → runs last
	t.Cleanup(func() {
		_ = clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(
			context.Background(), webhookName, metav1.DeleteOptions{},
		)
	})
}

// createSecret creates a Secret with a "tls.crt" key and registers cleanup.
func createSecret(t *testing.T, clientset *kubernetes.Clientset, name, certValue string) {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Data:       map[string][]byte{"tls.crt": []byte(certValue)},
	}
	_, err := clientset.CoreV1().Secrets(testNamespace).Create(context.Background(), secret, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = clientset.CoreV1().Secrets(testNamespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

// applyYAML runs `kubectl apply -f <path>`.
// Requires kubectl in PATH and KUBECONFIG set in the environment.
func applyYAML(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("kubectl", "apply", "-f", path)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "kubectl apply failed:\n%s", string(out))
}

// deleteYAML runs `kubectl delete -f <path> --ignore-not-found`.
func deleteYAML(path string) {
	cmd := exec.Command("kubectl", "delete", "-f", path, "--ignore-not-found")
	cmd.Env = os.Environ()
	_ = cmd.Run()
}

// waitForPodsRunning polls until all expectedCount pods for stsName are Running.
// Owns its own 2-minute timeout to avoid double-timeout confusion with a caller-supplied context.
func waitForPodsRunning(t *testing.T, clientset *kubernetes.Clientset, stsName string, expectedCount int) {
	t.Helper()
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 2*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			pods, err := clientset.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", stsName),
			})
			if err != nil {
				return false, nil // transient error, keep polling
			}
			running := 0
			for _, p := range pods.Items {
				if p.Status.Phase == corev1.PodRunning {
					running++
				}
			}
			t.Logf("pods running: %d/%d", running, expectedCount)
			return running == expectedCount, nil
		},
	)
	require.NoError(t, err, "timed out waiting for %d pods of %s to be Running", expectedCount, stsName)
}

// assertVolumeMount checks the pod spec for the injected per-pod-cert volume and volumeMount.
func assertVolumeMount(t *testing.T, clientset *kubernetes.Clientset, podName, expectedSecretName string) {
	t.Helper()
	pod, err := clientset.CoreV1().Pods(testNamespace).Get(context.Background(), podName, metav1.GetOptions{})
	require.NoError(t, err)

	foundVol := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == "per-pod-cert" && v.Secret != nil && v.Secret.SecretName == expectedSecretName {
			foundVol = true
			break
		}
	}
	assert.True(t, foundVol,
		"pod %s: expected volume per-pod-cert with secretName=%s\ngot: %+v",
		podName, expectedSecretName, pod.Spec.Volumes)

	require.NotEmpty(t, pod.Spec.Containers)
	foundMount := false
	for _, vm := range pod.Spec.Containers[0].VolumeMounts {
		if vm.Name == "per-pod-cert" && vm.MountPath == "/per-pod-cert" {
			foundMount = true
			break
		}
	}
	assert.True(t, foundMount,
		"pod %s: expected volumeMount per-pod-cert at /per-pod-cert\ngot: %+v",
		podName, pod.Spec.Containers[0].VolumeMounts)
}

// waitForPodLogs polls the cert-reader container logs until expectedContent appears or 60s elapses.
// Each call owns its own independent 60-second budget — no shared context starvation.
// This is more reliable than a fixed sleep because it handles slow image pulls and volume mount delays.
func waitForPodLogs(t *testing.T, clientset *kubernetes.Clientset, podName, expectedContent string) {
	t.Helper()
	// Each pod gets its own independent 60-second budget — no shared context starvation.
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 60*time.Second, true,
		func(ctx context.Context) (bool, error) {
			stream, err := clientset.CoreV1().Pods(testNamespace).GetLogs(podName, &corev1.PodLogOptions{
				Container: "cert-reader",
			}).Stream(ctx)
			if err != nil {
				return false, nil // pod may not be log-ready yet
			}
			defer stream.Close()
			body, err := io.ReadAll(stream)
			if err != nil {
				return false, nil
			}
			return strings.Contains(string(body), expectedContent), nil
		},
	)
	require.NoError(t, err, "pod %s: timed out waiting for %q in cert-reader logs", podName, expectedContent)
}
