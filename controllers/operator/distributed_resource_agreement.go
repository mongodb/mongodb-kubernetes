package operator

// F12b — every operator (leader and follower) hashes its local copies of the
// spec-referenced ConfigMaps / Secrets and submits them to the raft FSM. The
// reconcile is then gated on cross-cluster agreement: if any required ref has
// not been observed by all known clusters, or if reported hashes disagree, the
// reconcile returns workflow.Pending and the MDB status condition surfaces the
// drift diagnostic. The user must fix the underlying resource drift — the
// operator does not auto-resolve.
//
// Rationale: raft leader election rotates between clusters; divergent local
// copies of project ConfigMap / credentials Secret / TLS material would
// otherwise yield a "whichever cluster happens to be leader wins"
// inconsistency. The gate turns that into a hard "no progress until fixed".

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// CRSpecResourceKind is the ResourceRef.Kind value used by the iter-14e
// CR-spec agreement gate (Gate 0). The agreed hash is computed over the
// MongoDB CR's `.spec` only — not status, not metadata.generation, not
// managedFields — so two clusters that hold byte-identical specs reach
// agreement even if their per-cluster K8s metadata diverges (which it
// always does: resourceVersion, generation timestamps, etc. drift
// independently per apiserver).
const CRSpecResourceKind = "MongoDB"

// collectSpecReferencedResourceRefs returns the canonical list of K8s
// resources every operator must agree on before any of them touches OM. The
// list is built deterministically from the MongoDB spec, so each operator
// produces the same set in the same order (the FSM doesn't care about the
// order but the diagnostic prefers stable output).
//
// Included refs (operator-managed / shared by definition):
//   - MongoDB CR itself — added by G'5 iter 14e as Gate 0. The agreed hash
//     covers `.spec` only (no status, no per-cluster K8s metadata). This
//     blocks the reconcile from advancing while one cluster has observed a
//     newer spec via its local watch but the other clusters haven't yet —
//     the very window during which the iter-14e scale-up cap violation
//     surfaced (see iter-14e status). The check is operationally cheap
//     (one extra raft heartbeat) and prevents a real-world GitOps footgun:
//     manifests applied via per-cluster GitOps tooling can briefly disagree;
//     the gate replaces "leader wins" with "no one moves until all agree".
//   - Project ConfigMap (Spec.CloudManager.ConfigMapRef or
//     Spec.OpsManager.ConfigMapRef — surfaced via GetProjectConfigMapName).
//   - Credentials Secret (Spec.Credentials).
//
// Deliberately EXCLUDED (per G iter 8): TLS member/agent/prometheus
// certificate Secrets and LDAP bind-query / SCRAM agent-password Secrets.
// These are user-provided and the operator can't assume the user replicates
// byte-identical copies across clusters — TLS material in particular is
// commonly cluster-specific (different CA per cluster, or only one cluster
// holds the cert because the other clusters do not face external traffic).
// Forcing a hash agreement on TLS would block multi-cluster reconciles on
// configurations that work correctly today, so the gate is restricted to
// the truly shared inputs (CR spec / project / credentials).
//
// CA bundle: the project ConfigMap itself carries an optional
// `sslMMSCAConfigMap` field that names another ConfigMap with the CA. The
// referenced ConfigMap is read lazily (after the project CM hash is known),
// so it is not added here. The leader checks for it during downstream OM
// connection setup; F12 leaves that path unchanged. (TODO post-PoC: extend
// the agreed-set to include the CA CM by name once the project CM hash
// agrees.)
func collectSpecReferencedResourceRefs(sc *mdbv1.MongoDB) []coordination.ResourceRef {
	var refs []coordination.ResourceRef

	// G'5 iter 14e: CR-spec agreement gate. Added FIRST so the diagnostic
	// surfaces CR drift as the primary blocker (rather than buried below
	// a project-CM / credentials mismatch). The ref's Namespace + Name
	// are the CR's own location; the hash function reads
	// `r.sc.Spec` directly (not the live K8s object) so the value is
	// consistent within a single reconcile.
	refs = append(refs, coordination.ResourceRef{
		Kind: CRSpecResourceKind, Namespace: sc.Namespace, Name: sc.Name,
	})
	// Project ConfigMap.
	if name := sc.GetProjectConfigMapName(); name != "" {
		refs = append(refs, coordination.ResourceRef{
			Kind: "ConfigMap", Namespace: sc.GetProjectConfigMapNamespace(), Name: name,
		})
	}
	// Credentials Secret.
	if name := sc.GetCredentialsSecretName(); name != "" {
		refs = append(refs, coordination.ResourceRef{
			Kind: "Secret", Namespace: sc.GetCredentialsSecretNamespace(), Name: name,
		})
	}

	// TLS cert secrets and LDAP secrets are intentionally NOT part of the
	// agreement set. See function comment.

	// Sort for stable order across operators.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].Namespace != refs[j].Namespace {
			return refs[i].Namespace < refs[j].Namespace
		}
		return refs[i].Name < refs[j].Name
	})
	return refs
}

// hashCRSpec computes a stable SHA-256 over the MongoDB CR's `.spec` only.
// Status, metadata.generation, resourceVersion, managedFields are all
// excluded so two clusters whose CR specs are byte-identical produce the
// same hash regardless of per-cluster K8s metadata drift.
//
// G'5 iter 14f: the hash is canonicalised through the unstructured map
// representation, NOT the typed `*mdbv1.MongoDB` struct. The previous
// iter-14e implementation hashed `json.Marshal(sc.Spec)` directly, which
// produced different bytes per cluster despite identical wire-side spec
// JSON. The drift sources include:
//
//   - Pointer-vs-nil differences in optional pointer fields filled by
//     `MongoDB.UnmarshalJSON`'s `InitDefaults` (e.g. one cluster's
//     decoder may have observed an absent field as nil while another
//     populated an empty struct via InitDefaults, depending on watch
//     cache state).
//   - Empty-map / nil-map distinctions that survive a Go struct
//     round-trip but vanish under canonical JSON.
//   - `omitempty` quirks where two structurally equivalent values
//     marshal to different bytes depending on which struct field path
//     the value sits under.
//
// Canonical JSON of the unstructured map (sorted keys recursively) is
// stable across all these sources: it depends only on the value tree as
// seen on the wire. The conversion runs through
// `runtime.DefaultUnstructuredConverter.ToUnstructured(sc)` and the
// result's `.spec` subtree is canonicalised. We deliberately strip
// status / metadata before canonicalising — the agreement is over `.spec`
// only.
//
// If conversion fails (it shouldn't for a valid `*MongoDB`), we return
// a sentinel hash that will never match any real spec; the gate then
// stays Pending, which is the correct conservative behaviour.
func hashCRSpec(sc *mdbv1.MongoDB) string {
	if sc == nil {
		return "MISSING:MongoDB"
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sc)
	if err != nil {
		// Conversion errors are non-recoverable for the gate; encode the
		// error into the hash so a stable diagnostic surface still
		// includes "we couldn't hash, here's why" rather than silently
		// agreeing on garbage.
		return "ERR:ToUnstructured:" + err.Error()
	}
	spec, _ := u["spec"].(map[string]interface{})
	if spec == nil {
		// A CR with no `.spec` is degenerate but we should still produce
		// a stable hash so all clusters agree (every cluster sees the
		// same absence).
		spec = map[string]interface{}{}
	}
	canon, err := canonicalJSON(spec)
	if err != nil {
		return "ERR:CanonicalJSON:" + err.Error()
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON marshals an arbitrary JSON-decoded value (map / slice /
// scalar) to JSON bytes with map keys sorted recursively. This produces
// a byte-stable representation: two values that are JSON-equal but were
// reached through different code paths (typed-struct marshal vs raw
// unstructured) will canonicalise to the same bytes.
//
// Implementation: walk the value, rebuild with `json.Encoder` so we can
// disable HTML escaping, and use `json.Marshal` after canonicalising
// keys. The encoder appends a trailing newline; we trim it for hash
// stability against external test fixtures.
func canonicalJSON(v interface{}) ([]byte, error) {
	c, err := canonicalise(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	// Encoder appends a trailing newline — trim for hash stability.
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

// canonicalise rebuilds a JSON-decoded value with all maps sorted by
// key. Slices preserve order (JSON arrays are ordered). Scalars are
// returned as-is. Unknown types are an error: in practice the input is
// a result of `json.Unmarshal` / `runtime.DefaultUnstructuredConverter`
// so only map[string]interface{} / []interface{} / scalars should
// appear.
func canonicalise(v interface{}) (interface{}, error) {
	switch x := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// json.Marshal of map[string]interface{} sorts keys natively, so
		// we could just return x — BUT nested maps inside arbitrary
		// interface{} values may NOT be canonicalised by the encoder
		// unless we recurse and rebuild. Use a sorted slice-of-pairs
		// representation to force the order downstream.
		type kv struct {
			K string
			V interface{}
		}
		out := make(map[string]interface{}, len(keys))
		for _, k := range keys {
			cv, err := canonicalise(x[k])
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			ce, err := canonicalise(e)
			if err != nil {
				return nil, err
			}
			out[i] = ce
		}
		return out, nil
	case string, bool, float64, int, int32, int64, json.Number, nil:
		return v, nil
	default:
		// Fall back to JSON round-trip for unknown types (defensive —
		// shouldn't be reached for unstructured input).
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("canonicalise: unsupported type %T: %w", v, err)
		}
		var decoded interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			return nil, fmt.Errorf("canonicalise: round-trip failed for %T: %w", v, err)
		}
		return canonicalise(decoded)
	}
}

// hashConfigMapData computes a stable SHA-256 over the .data map of a
// ConfigMap. Map keys are sorted before hashing, so two ConfigMaps with
// identical contents but different key insertion order produce the same hash.
// Hashing only `.data` means we drop K8s-managed metadata (resourceVersion,
// uid, creationTimestamp, generation, managedFields, selfLink) automatically.
func hashConfigMapData(cm *corev1.ConfigMap) string {
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct{ K, V string }
	stable := make([]kv, 0, len(keys))
	for _, k := range keys {
		stable = append(stable, kv{K: k, V: cm.Data[k]})
	}
	// BinaryData too (rare but possible).
	binKeys := make([]string, 0, len(cm.BinaryData))
	for k := range cm.BinaryData {
		binKeys = append(binKeys, k)
	}
	sort.Strings(binKeys)
	type kvb struct {
		K string
		V []byte
	}
	binStable := make([]kvb, 0, len(binKeys))
	for _, k := range binKeys {
		binStable = append(binStable, kvb{K: k, V: cm.BinaryData[k]})
	}
	payload, _ := json.Marshal(struct {
		Data       []kv  `json:"data"`
		BinaryData []kvb `json:"binaryData"`
	}{Data: stable, BinaryData: binStable})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// hashSecretData computes a stable SHA-256 over the .data map of a Secret.
// Same canonicalisation as hashConfigMapData; .data values are already []byte
// in the typed K8s object, so we hash them directly (after base64 decoding by
// the K8s client). The Secret's Type is included so e.g. a TLS Secret with
// the same bytes but a different Type is still considered different.
func hashSecretData(s *corev1.Secret) string {
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kvb struct {
		K string
		V []byte
	}
	stable := make([]kvb, 0, len(keys))
	for _, k := range keys {
		stable = append(stable, kvb{K: k, V: s.Data[k]})
	}
	// StringData is normally empty after the apiserver round-trips, but
	// include it defensively for parity with kubectl-applied bytes.
	sdKeys := make([]string, 0, len(s.StringData))
	for k := range s.StringData {
		sdKeys = append(sdKeys, k)
	}
	sort.Strings(sdKeys)
	type kv struct{ K, V string }
	sdStable := make([]kv, 0, len(sdKeys))
	for _, k := range sdKeys {
		sdStable = append(sdStable, kv{K: k, V: s.StringData[k]})
	}
	payload, _ := json.Marshal(struct {
		Type       string `json:"type"`
		Data       []kvb  `json:"data"`
		StringData []kv   `json:"stringData"`
	}{Type: string(s.Type), Data: stable, StringData: sdStable})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// reportLocalResourceHash reads one ResourceRef from the local K8s client,
// computes its content-hash, and submits it to the coordinator. Missing
// resources are reported with a sentinel "MISSING:<kind>" hash so the gate
// remains correct: if cluster A has the resource and cluster B doesn't, the
// hashes will differ and WaitForResourcesAgreed will surface the diagnostic
// rather than silently proceeding.
func (r *ShardedClusterReconcileHelper) reportLocalResourceHash(ctx context.Context, ref coordination.ResourceRef, log *zap.SugaredLogger) error {
	if r.coordinator == nil {
		return nil
	}
	key := kube.ObjectKey(ref.Namespace, ref.Name)
	var hash string
	switch ref.Kind {
	case CRSpecResourceKind:
		// G'5 iter 14e: the CR-spec ref is satisfied by hashing
		// `r.sc.Spec` directly. We avoid a second K8s fetch — the helper
		// was constructed with the live CR for this reconcile, and the
		// downstream gate logic relies on the SAME spec snapshot being
		// what later code paths use. Hashing the helper's in-memory copy
		// guarantees the agreed-on hash matches the spec actually being
		// reconciled (a re-fetch would race against another writer and
		// could agree on a spec the operator never sees).
		if r.sc == nil {
			hash = "MISSING:MongoDB"
		} else {
			hash = hashCRSpec(r.sc)
		}
	case "ConfigMap":
		cm, err := r.commonController.client.GetConfigMap(ctx, key)
		if err != nil {
			if isNotFound(err) {
				hash = "MISSING:ConfigMap"
			} else {
				return err
			}
		} else {
			hash = hashConfigMapData(&cm)
		}
	case "Secret":
		s, err := r.commonController.client.GetSecret(ctx, key)
		if err != nil {
			if isNotFound(err) {
				hash = "MISSING:Secret"
			} else {
				return err
			}
		} else {
			hash = hashSecretData(&s)
		}
	default:
		// Unknown kind — should not happen for current refs. Skip silently.
		log.Debugf("Distributed mode: unknown resource kind %q for %s — skipping", ref.Kind, ref.String())
		return nil
	}
	if err := r.coordinator.ReportResource(r.crKeyFor(), ref, hash); err != nil {
		return err
	}
	return nil
}

// isNotFound is a small wrapper so callers can branch on "missing locally"
// without importing apierrors directly.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	// Unwrap (controller-runtime wraps with xerrors in some paths).
	for inner := err; inner != nil; {
		if apierrors.IsNotFound(inner) {
			return true
		}
		next := errors.Unwrap(inner)
		if next == inner {
			break
		}
		inner = next
	}
	return false
}

// gateOnResourceAgreement runs the F12b reconcile-top gate: every operator
// reports its local content-hashes for all spec-referenced refs, then waits
// for cross-cluster agreement. Returns workflow.OK iff no coordinator is
// attached (non-distributed mode) or every required ref is agreed across
// every known cluster; otherwise workflow.Pending with the diagnostic.
//
// On any error during local read / propose, the gate returns
// workflow.Pending with a transient message rather than Failed so the next
// reconcile retries the read.
//
// G'5 iter 14f: any path that returns Pending FIRST refreshes the
// HeartbeatAt on every lease this cluster currently holds for the CR (see
// refreshHeldLeases below). Without the refresh, a reconcile that
// repeatedly hits Pending at the gate during a legitimate spec-replication
// window will age out leases the cluster is still actively trying to
// advance — breaking the cross-cluster cap=1 serialisation invariant
// because the leader's stuck-step detector will revoke the lease and a
// different cluster will then acquire it concurrently with our own
// pending-but-still-real progress.
func (r *ShardedClusterReconcileHelper) gateOnResourceAgreement(ctx context.Context, log *zap.SugaredLogger) workflow.Status {
	if r.coordinator == nil {
		return workflow.OK()
	}
	refs := collectSpecReferencedResourceRefs(r.sc)
	if len(refs) == 0 {
		return workflow.OK()
	}
	for _, ref := range refs {
		if err := r.reportLocalResourceHash(ctx, ref, log); err != nil {
			log.Debugf("Distributed mode: failed to report local hash for %s: %v", ref.String(), err)
			r.refreshHeldLeases(log)
			return workflow.Pending("Distributed mode: failed to report local hash for %s: %v", ref.String(), err)
		}
	}
	ag, diag := r.coordinator.WaitForResourcesAgreed(r.crKeyFor(), refs)
	if ag != coordination.ResourcesAgreed {
		log.Infow("Distributed mode: resources not yet agreed across clusters", "diagnostic", diag)
		r.refreshHeldLeases(log)
		return workflow.Pending("ResourcesNotAgreed: %s", diag)
	}
	return workflow.OK()
}

// refreshHeldLeases emits a ReportProgress for every lease this cluster
// currently holds on the CR. ReportProgress hits the FSM's lease-merge
// path which refreshes `HeartbeatAt` on the matching lease — equivalent
// to a keep-alive — without changing any Ready bit (it's Ready=false on
// the wire). The leader's stuck-step detector therefore continues to see
// the holder as "actively trying" even while we're parked at a top-of-
// reconcile gate.
//
// Idempotent and best-effort: errors are logged at Debug and not
// returned. The caller is already on a Pending return path; failing the
// keep-alive can't change the gate verdict.
//
// We intentionally use ProgressSnapshot zero-values for replica counts:
// we don't have current/ready replica information at the gate-wait site
// (we haven't read the local STS yet for this reconcile). The leader's
// stuck-step detector compares ProgressSnapshot signature equality
// across reports to decide "stuck" — emitting zeros doesn't introduce
// false motion because the downstream `distReportInflightProgress` call
// (once the gate clears) reports the actual replica state. What we need
// here is purely the HeartbeatAt refresh, which fires on any successful
// status report regardless of payload.
func (r *ShardedClusterReconcileHelper) refreshHeldLeases(log *zap.SugaredLogger) {
	if r.coordinator == nil {
		return
	}
	myCluster := r.coordinator.MyClusterName()
	if myCluster == "" {
		return
	}
	components := r.coordinator.GetLeasesHeldBy(r.crKeyFor(), myCluster)
	if len(components) == 0 {
		return
	}
	crGen := int64(0)
	if r.sc != nil {
		crGen = r.sc.GetGeneration()
	}
	for _, component := range components {
		progress := coordination.ProgressSnapshot{
			CRSpecGeneration: crGen,
		}
		if err := r.coordinator.ReportProgress(r.crKeyFor(), component, myCluster, progress); err != nil {
			log.Debugf("Distributed mode: refreshHeldLeases ReportProgress(%s,%s) failed: %v", component, myCluster, err)
		}
	}
	log.Debugf("Distributed mode: refreshed %d held lease heartbeat(s) on cluster %s during gate wait: %v",
		len(components), myCluster, components)
}
