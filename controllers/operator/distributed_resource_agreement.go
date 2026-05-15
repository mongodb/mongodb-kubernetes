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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/coordination"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// collectSpecReferencedResourceRefs returns the canonical list of K8s
// resources every operator must agree on before any of them touches OM. The
// list is built deterministically from the MongoDB spec, so each operator
// produces the same set in the same order (the FSM doesn't care about the
// order but the diagnostic prefers stable output).
//
// Included refs:
//   - Project ConfigMap (Spec.CloudManager.ConfigMapRef or
//     Spec.OpsManager.ConfigMapRef — surfaced via GetProjectConfigMapName).
//   - Credentials Secret (Spec.Credentials).
//   - Member certificate Secrets, agent certificate Secret, prometheus
//     certificate Secret (only when TLS is enabled / CertificatesSecretsPrefix
//     is set).
//   - LDAP bind-query / agent-password Secrets if authentication references
//     them.
//
// CA bundle: the project ConfigMap itself carries an optional
// `sslMMSCAConfigMap` field that names another ConfigMap with the CA. The
// referenced ConfigMap is read lazily (after the project CM hash is known),
// so it is not added here. The leader checks for it during downstream OM
// connection setup; F12 leaves that path unchanged. (TODO post-PoC: extend
// the agreed-set to include the CA CM by name once the project CM hash
// agrees.)
func collectSpecReferencedResourceRefs(sc *mdbv1.MongoDB) []coordination.ResourceRef {
	ns := sc.GetNamespace()
	var refs []coordination.ResourceRef

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

	// Member / agent certs are only consulted when TLS / certificate prefix
	// is in use. We add only the secret names this CR actually references.
	sec := sc.GetSecurity()
	if sec != nil && (sec.IsTLSEnabled() || sec.CertificatesSecretsPrefix != "") {
		seen := map[string]struct{}{}
		addSecret := func(name string) {
			if name == "" {
				return
			}
			if _, ok := seen[name]; ok {
				return
			}
			seen[name] = struct{}{}
			refs = append(refs, coordination.ResourceRef{Kind: "Secret", Namespace: ns, Name: name})
		}
		if sc.Spec.ResourceType == mdbv1.ShardedCluster {
			for i := 0; i < sc.Spec.ShardCount; i++ {
				addSecret(sec.MemberCertificateSecretName(sc.ShardRsName(i)))
			}
			addSecret(sec.MemberCertificateSecretName(sc.ConfigRsName()))
			addSecret(sec.MemberCertificateSecretName(sc.MongosRsName()))
		} else {
			addSecret(sec.MemberCertificateSecretName(sc.Name))
		}
		addSecret(sec.AgentClientCertificateSecretName(sc.Name))
	}

	// LDAP / SCRAM agent secrets when referenced from spec.
	if sec != nil && sec.Authentication != nil {
		if sec.Authentication.Ldap != nil && sec.Authentication.Ldap.BindQuerySecretRef.Name != "" {
			refs = append(refs, coordination.ResourceRef{
				Kind: "Secret", Namespace: ns, Name: sec.Authentication.Ldap.BindQuerySecretRef.Name,
			})
		}
		if sec.Authentication.Agents.AutomationPasswordSecretRef.Name != "" {
			refs = append(refs, coordination.ResourceRef{
				Kind: "Secret", Namespace: ns, Name: sec.Authentication.Agents.AutomationPasswordSecretRef.Name,
			})
		}
	}

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
			return workflow.Pending("Distributed mode: failed to report local hash for %s: %v", ref.String(), err)
		}
	}
	ag, diag := r.coordinator.WaitForResourcesAgreed(r.crKeyFor(), refs)
	if ag != coordination.ResourcesAgreed {
		log.Infow("Distributed mode: resources not yet agreed across clusters", "diagnostic", diag)
		return workflow.Pending("ResourcesNotAgreed: %s", diag)
	}
	return workflow.OK()
}
