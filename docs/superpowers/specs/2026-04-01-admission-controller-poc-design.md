# Admission Controller PoC Design

**Date:** 2026-04-01  
**Author:** Maciej Karaś  
**Related:** CLOUDP-389489 — Automate TLS certificates with cert-manager  

## Problem

Kubernetes StatefulSets use a single uniform pod template — there is no native way to mount a different Secret per pod replica. The goal of this PoC is to validate **Option B** from the design document: a Spoditor-style mutating admission webhook that intercepts Pod CREATE requests and injects per-pod Secret volume mounts based on the pod's ordinal.

## Approach

A Go HTTP server implementing a Kubernetes Mutating Admission Webhook. It intercepts Pod admission for targeted StatefulSets, extracts the pod ordinal from the pod name, and patches the pod spec to mount the correct per-pod Secret. The webhook runs **outside the cluster** (developer machine) and is registered via a `MutatingWebhookConfiguration` pointing to the host's IP.

## Targeting Mechanism

- **Label** `per-pod-secret-webhook/enabled: "true"` on the StatefulSet pod template — used as `objectSelector` in the `MutatingWebhookConfiguration` so Kubernetes pre-filters pods before calling the webhook.
- **Annotation** `per-pod-secret-webhook/secret-name-prefix: "<prefix>"` on the pod template — the webhook appends `-<ordinal>` to build the secret name (e.g., prefix `my-sts` + ordinal `0` → secret `my-sts-0`).

## Directory Structure

```
poc/admission-webhook/
├── webhook.go         # HTTP handler: AdmissionReview decode, JSON patch logic
├── server.go          # HTTPS server setup, TLS cert generation
├── webhook_test.go    # integration test
└── testdata/
    └── sts.yaml       # headless Service + StatefulSet (no Secrets — test creates them)
```

Lives under the existing Go module (`github.com/mongodb/mongodb-kubernetes`). Reuses `pkg/webhook/certificates.go` for self-signed TLS cert generation.

## Webhook Handler (`webhook.go`)

Single `http.HandlerFunc` on `/mutate-pods`.

**Key Go imports:**
- `admissionv1 "k8s.io/api/admission/v1"` — for `AdmissionReview`, `AdmissionRequest`, `AdmissionResponse`, `PatchTypeJSONPatch` (distinct from `admissionregistration/v1` which is used only for webhook configuration objects)
- `admissionregistrationv1 "k8s.io/api/admissionregistration/v1"` — for `MutatingWebhookConfiguration` registration only

**Handler steps:**

1. Decode `admissionv1.AdmissionReview` from request body.
2. Extract pod from `request.Object` using `k8s.io/apimachinery/pkg/runtime/serializer`.
3. If pod lacks label `per-pod-secret-webhook/enabled: "true"` → return allow with no patch (safe fallback; shouldn't happen due to `objectSelector` pre-filtering).
4. Read annotation `per-pod-secret-webhook/secret-name-prefix` from pod — if missing, return deny with a descriptive message.
5. Extract ordinal by splitting pod name on `-` and taking the last segment. Validate with `strconv.Atoi` — if the last segment is not a valid non-negative integer, return a denial response with a clear error message.
6. Build `application/json-patch+json` patch (RFC 6902):
   - Append to `spec.volumes`: `{name: "per-pod-cert", secret: {secretName: "<prefix>-<ordinal>"}}`
   - For each container at index `i`, append to `spec.containers[i].volumeMounts`: `{name: "per-pod-cert", mountPath: "/per-pod-cert", readOnly: true}`
7. Return `admissionv1.AdmissionResponse` with:
   - `Allowed: true`
   - `Patch`: JSON-encoded patch bytes
   - `PatchType`: pointer to `admissionv1.PatchTypeJSONPatch` (required — omitting this causes the API server to ignore the patch)

## Server (`server.go`)

- Generates a self-signed TLS cert via `pkg/webhook/certificates.go` (`createSelfSignedCert` returns raw DER bytes; `server.go` PEM-encodes them before writing and before returning `certPEM`).
- `certPEM` returned by `Start` is always **PEM-encoded** — this is required by the `caBundle` field in `MutatingWebhookConfiguration`.
- Starts HTTPS listener on the given address.
- Exposes `Start(addr string) (certPEM []byte, actualAddr string, err error)` for use in tests.

## Sample StatefulSet YAML (`testdata/sts.yaml`)

Two objects:

1. **Headless Service** `my-sts` — required by `serviceName`.
2. **StatefulSet** `my-sts`, 3 replicas:
   - Pod template label: `per-pod-secret-webhook/enabled: "true"`
   - Pod template annotation: `per-pod-secret-webhook/secret-name-prefix: "my-sts"`
   - Single `busybox` container: loops waiting for `/per-pod-cert/tls.crt`, prints hostname + contents, sleeps 15s.
   - No volumes or volumeMounts defined — the webhook injects them at admission time.

Per-pod Secrets (`my-sts-0`, `my-sts-1`, `my-sts-2`) are created programmatically in the test.

## Integration Test (`webhook_test.go`)

`TestAdmissionWebhookIntegration` steps:

1. **Start server** — call `server.Start(":0")`, get PEM-encoded cert and bound address.
2. **Auto-detect host IP** — list cluster nodes via client-go, read the first `InternalIP` from `status.addresses`. On macOS with kind (Docker Desktop), `InternalIP` is the container's internal network address which is not reachable from the host. To handle this: attempt a TCP dial to `<nodeInternalIP>:<port>` with a short timeout; if that fails, fall back to `host.docker.internal` (which Docker Desktop resolves to the host machine). This ensures the test works on both Linux (direct IP) and macOS (host.docker.internal).
3. **Register webhook** — create `MutatingWebhookConfiguration` with:
   - `name`: `per-pod-secret.mongodb.com` (a valid FQDN with ≥ 2 dots, as required)
   - `webhooks[0].name`: `per-pod-secret.mongodb.com`
   - `clientConfig.url`: `https://<resolved-host>:<port>/mutate-pods`
   - `caBundle`: PEM-encoded cert from step 1
   - `objectSelector`: match label `per-pod-secret-webhook/enabled: "true"`
   - `namespaceSelector`: match the test namespace (limits blast radius to the test namespace only)
   - `rules`: Pod CREATE, API group `""` (core), resource `pods`
   - `admissionReviewVersions`: `["v1"]` (required field)
   - `failurePolicy`: `Ignore` (appropriate for a PoC; avoids breaking the cluster if the webhook process becomes unreachable mid-test)
4. **Register cleanup immediately after webhook creation** — `t.Cleanup` defers deletion of the `MutatingWebhookConfiguration` as the very next step after successful registration, before any further test actions that could fail and leave it behind.
5. **Create Secrets** — `my-sts-0`, `my-sts-1`, `my-sts-2` each with a distinct `tls.crt` value. Also register `t.Cleanup` to delete them.
6. **Apply StatefulSet** from `testdata/sts.yaml`. Register `t.Cleanup` to delete it.
7. **Wait for pods** — poll until all 3 pods are `Running` (2 min timeout).
8. **Assert volume mounts** — for each pod verify:
   - `spec.volumes` contains a volume named `per-pod-cert` with `secretName` matching `my-sts-<ordinal>`.
   - `spec.containers[0].volumeMounts` contains `{name: "per-pod-cert", mountPath: "/per-pod-cert"}`.
9. **Assert logs** — check container logs contain the pod's expected cert value.

## Key Design Decisions

| Decision | Choice | Reason |
|---|---|---|
| What the webhook intercepts | Pod CREATE | StatefulSet template is uniform; per-pod mutation only possible at Pod admission |
| Targeting | Label on pod template | Enables `objectSelector` pre-filtering in kube; no unnecessary webhook calls |
| Secret name config | Annotation with prefix | Decouples secret naming from STS name; webhook appends ordinal |
| Mount path | Fixed `/per-pod-cert` | Sufficient for PoC; avoids extra annotation complexity |
| Test infrastructure | Real cluster | Needed for pod scheduling + log verification |
| Host IP detection | Dial test then fallback to `host.docker.internal` | Works for both Linux (direct IP) and macOS kind |
| TLS | Self-signed, PEM-encoded, auto-generated | Reuses existing `pkg/webhook/certificates.go` |
| `failurePolicy` | `Ignore` | Prevents cluster-wide pod scheduling failures if webhook becomes unreachable mid-test |
| Cleanup ordering | `t.Cleanup` registered immediately after each resource creation | Guarantees no dangling resources even if test panics |
| AdmissionReview package | `k8s.io/api/admission/v1` | Correct package for request/response cycle; `admissionregistration/v1` is for configuration objects only |
| `PatchType` | Always set to `PatchTypeJSONPatch` | Required by API server; omitting it causes the patch to be silently ignored |
