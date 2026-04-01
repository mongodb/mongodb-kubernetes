# Admission Controller PoC Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Mutating Admission Webhook in Go that intercepts Pod CREATE requests and injects per-pod TLS Secret volume mounts based on pod ordinal, validated by a full integration test against a real Kubernetes cluster.

**Architecture:** A Go HTTPS server handles `/mutate-pods` admission requests. It reads a secret-name-prefix annotation from the pod, extracts the ordinal from the pod name, and patches `spec.volumes` and `spec.containers[*].volumeMounts` to mount the pod-specific Secret at `/per-pod-cert`. The integration test starts the server, registers a `MutatingWebhookConfiguration`, deploys a StatefulSet, and asserts that each pod received the correct volume mount.

**Tech Stack:** Go, `k8s.io/api/admission/v1`, `k8s.io/client-go v0.33`, `github.com/stretchr/testify`, existing `pkg/webhook/certificates.go` for TLS cert generation.

---

## File Map

| File | Responsibility |
|---|---|
| `poc/admission-webhook/server.go` | Start HTTPS server, generate self-signed TLS cert via `pkg/webhook/certificates.go`, return PEM cert and bound address |
| `poc/admission-webhook/webhook.go` | Decode `AdmissionReview`, extract ordinal, build JSON patch, return `AdmissionResponse` |
| `poc/admission-webhook/webhook_test.go` | Integration test: detect host IP, start server, register webhook, deploy STS, assert volume mounts + logs |
| `poc/admission-webhook/testdata/sts.yaml` | Headless Service + 3-replica StatefulSet with webhook label/annotation, no pre-defined volumes |

All files live under the existing `github.com/mongodb/mongodb-kubernetes` module. Package name: `admissionwebhook`.

---

## Chunk 1: Foundation — server.go and webhook.go

### Task 1: Create `server.go`

**Files:**
- Create: `poc/admission-webhook/server.go`

- [ ] **Step 1.1: Write `server.go`**

```go
package admissionwebhook

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mongodb/mongodb-kubernetes/pkg/webhook"
)

// Start starts a TLS HTTP server on addr (use ":0" for a random free port).
// hosts is the list of IP addresses or hostnames to include in the cert's SAN —
// must include the IP the Kubernetes API server will use to reach this process.
// Returns the PEM-encoded CA cert, the actual bound address (host:port), and any error.
func Start(addr string, hosts []string, mux *http.ServeMux) (certPEM []byte, actualAddr string, err error) {
	certDir, err := os.MkdirTemp("", "webhook-certs-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp cert dir: %w", err)
	}

	if err := webhook.CreateCertFiles(hosts, certDir); err != nil {
		return nil, "", fmt.Errorf("creating cert files: %w", err)
	}

	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	certPEM, err = os.ReadFile(certPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading cert PEM: %w", err)
	}

	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("loading TLS key pair: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("listening on %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	go func() { _ = srv.ServeTLS(ln, "", "") }()

	return certPEM, ln.Addr().String(), nil
}
```

---

### Task 2: Create `webhook.go`

**Files:**
- Create: `poc/admission-webhook/webhook.go`

- [ ] **Step 2.1: Write `webhook.go`**

```go
package admissionwebhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	labelEnabled     = "per-pod-secret-webhook/enabled"
	annotationPrefix = "per-pod-secret-webhook/secret-name-prefix"
	volumeName       = "per-pod-cert"
	mountPath        = "/per-pod-cert"
)

// HandleMutatePods is the http.HandlerFunc for the /mutate-pods webhook endpoint.
func HandleMutatePods(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading body: %v", err), http.StatusBadRequest)
		return
	}

	ar := admissionv1.AdmissionReview{}
	if err := json.Unmarshal(body, &ar); err != nil {
		http.Error(w, fmt.Sprintf("decoding AdmissionReview: %v", err), http.StatusBadRequest)
		return
	}

	// Guard against malformed requests with nil Request field
	if ar.Request == nil {
		http.Error(w, "nil AdmissionRequest", http.StatusBadRequest)
		return
	}

	response := mutate(ar.Request)
	response.UID = ar.Request.UID
	ar.Response = response

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ar); err != nil {
		http.Error(w, fmt.Sprintf("encoding response: %v", err), http.StatusInternalServerError)
	}
}

func mutate(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	pod := corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return denyResponse(fmt.Sprintf("failed to decode pod: %v", err))
	}

	// Safe fallback: allow pods without the label (objectSelector should pre-filter, but be defensive)
	if pod.Labels[labelEnabled] != "true" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	prefix := pod.Annotations[annotationPrefix]
	if prefix == "" {
		return denyResponse(fmt.Sprintf("missing or empty annotation %q", annotationPrefix))
	}

	parts := strings.Split(pod.Name, "-")
	ordinalStr := parts[len(parts)-1]
	ordinal, err := strconv.Atoi(ordinalStr)
	if err != nil || ordinal < 0 {
		return denyResponse(fmt.Sprintf("pod name %q does not end with a valid non-negative ordinal", pod.Name))
	}

	secretName := fmt.Sprintf("%s-%d", prefix, ordinal)
	patchBytes, err := buildPatches(pod, secretName)
	if err != nil {
		return denyResponse(fmt.Sprintf("building patches: %v", err))
	}

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

func buildPatches(pod corev1.Pod, secretName string) ([]byte, error) {
	type jsonPatch struct {
		Op    string      `json:"op"`
		Path  string      `json:"path"`
		Value interface{} `json:"value"`
	}

	newVolume := map[string]interface{}{
		"name":   volumeName,
		"secret": map[string]interface{}{"secretName": secretName},
	}
	newMount := map[string]interface{}{
		"name":      volumeName,
		"mountPath": mountPath,
		"readOnly":  true,
	}

	var patches []jsonPatch

	// Add volume — initialize array if volumes is nil/empty; append otherwise.
	// Using /spec/volumes/- on a nil array produces a 422 from the API server.
	if len(pod.Spec.Volumes) == 0 {
		patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes", Value: []interface{}{newVolume}})
	} else {
		patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: newVolume})
	}

	// Add volumeMount to each container (same nil-vs-append logic)
	for i, c := range pod.Spec.Containers {
		if len(c.VolumeMounts) == 0 {
			patches = append(patches, jsonPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", i),
				Value: []interface{}{newMount},
			})
		} else {
			patches = append(patches, jsonPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", i),
				Value: newMount,
			})
		}
	}

	return json.Marshal(patches)
}

func denyResponse(reason string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result:  &metav1.Status{Message: reason},
	}
}
```

---

### Task 3: Verify compilation

**Files:** none new

- [ ] **Step 3.1: Compile the package**

```bash
cd /path/to/mongodb-kubernetes
go build ./poc/admission-webhook/...
```

Expected: no errors. If you see `undefined: webhook.CreateCertFiles`, check that the import path is `github.com/mongodb/mongodb-kubernetes/pkg/webhook`.

- [ ] **Step 3.2: Commit**

```bash
git add poc/admission-webhook/server.go poc/admission-webhook/webhook.go
git commit -m "poc: add admission webhook server and handler"
```

---

## Chunk 2: Test data and integration test

### Task 4: Create `testdata/sts.yaml`

**Files:**
- Create: `poc/admission-webhook/testdata/sts.yaml`

- [ ] **Step 4.1: Write `testdata/sts.yaml`**

```yaml
---
apiVersion: v1
kind: Service
metadata:
  name: my-sts
  namespace: default
spec:
  clusterIP: None
  selector:
    app: my-sts
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-sts
  namespace: default
spec:
  replicas: 3
  serviceName: my-sts
  selector:
    matchLabels:
      app: my-sts
  template:
    metadata:
      labels:
        app: my-sts
        "per-pod-secret-webhook/enabled": "true"
      annotations:
        "per-pod-secret-webhook/secret-name-prefix": "my-sts"
    spec:
      containers:
        - name: cert-reader
          image: busybox
          command: ["/bin/sh", "-c"]
          args:
            - |
              while true; do
                if [ -f /per-pod-cert/tls.crt ]; then
                  echo "=== $(hostname) cert ==="
                  cat /per-pod-cert/tls.crt
                else
                  echo "Waiting for cert..."
                fi
                sleep 15
              done
```

Note: no `volumes` or `volumeMounts` — the webhook injects them at Pod admission time.

- [ ] **Step 4.2: Commit**

```bash
git add poc/admission-webhook/testdata/sts.yaml
git commit -m "poc: add sample StatefulSet YAML for admission webhook test"
```

---

### Task 5: Create `webhook_test.go`

**Files:**
- Create: `poc/admission-webhook/webhook_test.go`

- [ ] **Step 5.1: Write `webhook_test.go`**

```go
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

	// 8. Poll pod logs until the expected cert content appears (or 60s timeout)
	logCtx, logCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer logCancel()
	for ordinal := 0; ordinal < 3; ordinal++ {
		podName := fmt.Sprintf("%s-%d", stsName, ordinal)
		waitForPodLogs(t, logCtx, clientset, podName, secretValues[podName])
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

// waitForPodLogs polls the cert-reader container logs until expectedContent appears or ctx expires.
// This is more reliable than a fixed sleep because it handles slow image pulls and volume mount delays.
func waitForPodLogs(t *testing.T, ctx context.Context, clientset *kubernetes.Clientset, podName, expectedContent string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 60*time.Second, true,
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
```

---

### Task 6: Build and run the integration test

- [ ] **Step 6.1: Verify compilation**

```bash
cd /path/to/mongodb-kubernetes
go build ./poc/admission-webhook/...
```

Expected: no compilation errors.

- [ ] **Step 6.2: Source the cluster kubeconfig**

```bash
source .generated/context.export.env
```

- [ ] **Step 6.3: Run the integration test**

```bash
go test ./poc/admission-webhook/... -v -run TestAdmissionWebhookIntegration -timeout 5m
```

Expected output (abbreviated):
```
=== RUN   TestAdmissionWebhookIntegration
    webhook_test.go: using host IP: host.docker.internal
    webhook_test.go: webhook URL: https://host.docker.internal:XXXXX/mutate-pods
    webhook_test.go: pods running: 0/3
    webhook_test.go: pods running: 3/3
--- PASS: TestAdmissionWebhookIntegration
PASS
```

**Troubleshooting:**
- `FailedMount` in `kubectl describe pod my-sts-0`: Secret doesn't exist — check Step 5.1 secret creation
- Pods stuck in `Pending`: admission webhook may be denying — check `kubectl get events` and test stdout
- TLS handshake errors in test output: cert SAN doesn't match webhook URL host — `detectHostIP` may have picked the wrong address

- [ ] **Step 6.4: Commit**

```bash
git add poc/admission-webhook/webhook_test.go
git commit -m "poc: add integration test for admission webhook"
```

---

## Notes for Implementors

**JSON patch edge cases:**
- When `spec.volumes` is `nil`/empty, the patch must initialize it as an array: `{"op":"add","path":"/spec/volumes","value":[{...}]}`. Using `/-` on a nil array causes a 422 from the API server.
- Same applies to `spec.containers[N].volumeMounts`.

**TLS cert SAN:**
- The cert is generated with the detected host IP or `host.docker.internal` in the SAN. The Kubernetes API server validates the cert against the hostname in the webhook URL — they must match.
- `pkg/webhook/certificates.go` handles both IP and DNS name SANs automatically.

**`failurePolicy: Ignore`:**
- Set to `Ignore` for the PoC. If the webhook server is unreachable, pod creation proceeds without mutation. This prevents blocking the cluster if the test process crashes.

**Cleanup order (LIFO):**
- `t.Cleanup` runs in LIFO order. Webhook config cleanup is registered first → runs last. StatefulSet and Secrets cleanup registered later → run first. This is intentional: the webhook stays registered until all other resources are cleaned up.

**Host IP detection — port 6443 assumption:**
- `detectHostIP` dials `nodeIP:6443` to test reachability. This assumes the kube API server is on port 6443, which is the default for kind. If using a different distribution (k3s, kubeadm with non-default port), this heuristic may incorrectly fall back to `host.docker.internal` on a Linux host. In that case, set `WEBHOOK_HOST` manually or adjust the probe port.

**Working directory for `testdata/sts.yaml`:**
- `go test` sets the working directory to the package directory (`poc/admission-webhook/`). The relative path `testdata/sts.yaml` is therefore correct when running via `go test ./poc/admission-webhook/...`. Do not run the compiled test binary from a different directory.
