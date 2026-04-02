# Per-Pod TLS Certificate Options: Comparison

**Author:** Maciej Karaś
**Date:** 2026-04-02
**Jira Epic:** CLOUDP-389489

## Background

MCK currently requires users to manually create a single TLS certificate shared across all StatefulSet members. Because a StatefulSet pod template is uniform — Kubernetes provides no native mechanism to mount different Secrets per replica — this shared certificate must list all member hostnames in its SAN, or use DNS wildcards. As deployments scale or hostnames change (external domains, split horizon, multi-cluster), users must manually update the certificate, triggering a restart of all mongod processes.

This document compares four approaches for automating per-Pod (per-Process) TLS certificates, enabling individual renewal and tighter SANs without affecting other members.

**Related docs:**
- [SPIKE: Automate TLS certificates with cert-manager](https://docs.google.com/document/d/1-eA4ZZRnqLkvTsgo6X4WoEt2_fIZqHBRHz4gLdyb2_0/edit?tab=t.npponz9enczw)
- [PD+Scope: MCK: Automate TLS certificates with cert-manager](https://docs.google.com/document/d/1AQ7QqH0ZAtL4OzxD7VDU6YhSlZ3vY57bcAPczWbbP-k/edit?tab=t.0)
- [Per-Pod Secrets in StatefulSet investigation](https://docs.google.com/document/d/1AQ7QqH0ZAtL4OzxD7VDU6YhSlZ3vY57bcAPczWbbP-k/edit?tab=t.2n8xeksrynv)
- [PD+Scope: Agent rotateCertificates](https://docs.google.com/document/d/1tAuFq3x9Fapv55ZG0jQbAERld2tQtLiso6HiM1xZwLo/edit?tab=t.0)

---

## Options

| # | Name | Short description |
|---|------|------------------|
| 1 | **Uber Secret** | Combined PEM secret with all pods' certs; each pod reads its own entry by pod name |
| 2 | **Uber Secret + Sidecar** | Same uber secret; sidecar copies pod-specific cert to `emptyDir` |
| 3 | **Admission Webhook** | Mutating webhook patches pod spec at creation to mount per-pod K8s Secrets |
| 4 | **CSI Driver + Agent Changes** | cert-manager CSI driver mounts certs directly into pods; Agent implements `rotateCertificates` |

---

## Comparison Table

| Dimension | 1: Uber Secret | 2: Uber Secret + Sidecar | 3: Admission Webhook | 4: CSI Driver + Agent |
|-----------|---------------|--------------------------|---------------------|----------------------|
| **Key isolation** | None — all pods mount all private keys | Partial — sidecar has all keys; main container sees only its own via `emptyDir` | Full — each pod mounts only its own K8s Secret | Full with cert-manager CSI (keys in-memory, per-pod). With Vault CSI: all certs visible inside pod unless per-pod Vault identity configured — keys never in etcd regardless |
| **Secret storage in etcd** | Yes — all certs in one Secret | Yes — all certs in one Secret | Yes — N separate Secrets (one per pod) | No — certs never materialise as K8s Secrets |
| **Blast radius if pod compromised** | All member keys exposed | All member keys exposed to sidecar; main container limited to its own | Only that pod's key exposed | With cert-manager CSI: only that pod's key. With Vault CSI (all-certs-mounted): all keys exposed in-pod but not in etcd |
| **Vault integration** | Complex — requires `writeToFile` Consul template loop or `.Ordinal`-based template to assemble uber secret | Same as Option 1 | Works natively if Vault creates per-pod Secrets matching the `<prefix>-<ordinal>` naming convention | Supported via Secrets Store CSI Driver — all per-pod certs mounted in pod from Vault paths; no K8s Secret intermediary in etcd. With per-pod Vault identity each pod gets only its own cert; without it, all certs visible in pod (similar access profile to Option 1 but without etcd storage) |
| **Multi-cluster support** | cert-manager installed in each cluster; operator creates Certificate resources locally — no cross-cluster secret copying | Same as Option 1 | cert-manager installed in each cluster; operator creates Certificate resources locally — no copying. **Additional burden: admission webhook must be deployed and maintained in every member cluster** | cert-manager + CSI driver installed in each cluster; no Certificate or K8s Secret resources created at all — cleanest multi-cluster story |
| **External dependencies** | None | None | MutatingWebhookConfiguration + HA webhook server in every cluster; webhook TLS cert needs its own rotation | cert-manager + cert-manager CSI driver (or Secrets Store CSI for Vault); MongoDB Agent `rotateCertificates` implementation already in progress ([PD+Scope](https://docs.google.com/document/d/1tAuFq3x9Fapv55ZG0jQbAERld2tQtLiso6HiM1xZwLo/edit?tab=t.0)); SERVER-109921 workaround needed for PEM combination |
| **Scale up/down impact** | Operator requests new per-pod Certificate from cert-manager; cert-manager creates Secret; Operator combines all per-pod certs into updated uber PEM secret | Same as Option 1 | Operator requests new per-pod Certificate from cert-manager; cert-manager creates separate PEM Secret per pod; **webhook must also be running** when new pod is admitted | CSI driver handles new pod automatically via volume attributes; no operator pre-work required |
| **Certificate rotation/renewal** | cert-manager renews pod's Certificate → updates K8s Secret → Operator updates uber PEM secret → Automation Config path changes → **only that pod's mongod process restarts** (current agent behaviour; other pods unaffected) | Same as Option 1; sidecar detects updated `emptyDir` on next poll cycle, then mongod process restarts for that pod only | cert-manager renews pod's Certificate → updates per-pod Secret → Automation Config path changes → **only that pod's mongod process restarts** (current agent behaviour); webhook not involved in renewal | CSI driver renews and updates cert **in place** in the pod's in-memory volume → Agent detects file change → calls `rotateCertificates` → **no mongod process restart required** (requires Agent implementation — [PD+Scope](https://docs.google.com/document/d/1tAuFq3x9Fapv55ZG0jQbAERld2tQtLiso6HiM1xZwLo/edit?tab=t.0)) |
| **K8s Secret size limit** | 1 MB hard limit; ~300–500 certs possible; MongoDB's 50-node limit never exceeded | Same as Option 1 | N small Secrets, each ≤3 KB; no practical limit | N/A |
| **Possible issues** | None; proven pattern (ECK uses uber-secret) | Race condition: sidecar must copy cert before main container reads; `emptyDir` lost on pod restart (sidecar re-copies at startup) | Webhook HA is critical: `failurePolicy: Ignore` → pods start without cert mount; `failurePolicy: Fail` → webhook outage blocks all matching pod creations cluster-wide. Webhook cert needs its own rotation. Customers sometimes disable webhooks. | Certs not persisted through node restart (new cert auto-issued); cert config changes (duration, subject) require pod recreation; pod crashloop makes cert unreachable |
| **Operator required for scale up/down** | Yes — to update uber secret | Yes — to update uber secret | Yes — must pre-create pod Secret **and** webhook must be running simultaneously | No — CSI driver handles injection autonomously |
| **Ease to debug** | Easy — inspect single Secret; all certs visible and survive pod death | Moderate — must check uber Secret + `emptyDir` inside pod + sidecar logs; `emptyDir` lost on pod restart | Moderate — trace through webhook admission events, MutatingWebhookConfiguration, webhook server logs, pod events; per-pod Secrets are inspectable | Hard — cert exists only during pod lifetime as in-memory volume; if pod crashloops cert is inaccessible; no Secret to inspect independently |
| **Ease to maintain** | Low — no new components; operator manages one Secret; existing patterns reused | Medium — sidecar container image must be maintained; sidecar failures need handling | High — HA webhook in every cluster, webhook TLS cert rotation, RBAC, network policies, K8s version compatibility | Medium → Low — no cert secrets to clean up; harder to support non-cert-manager users during transition |
| **Cost of development** | **Medium** — operator changes to create/manage uber PEM secret with per-pod entries; cleanup on scale-down; reuses existing hash-based patterns | **Medium-High** — same as Option 1 plus sidecar container implementation, image maintenance, and lifecycle management | **High** — HA webhook server in every cluster, TLS cert rotation for webhook itself, admission handler, operator integration for pre-creating per-pod Secrets, testing across K8s versions and environments | **Medium** — operator changes to integrate CSI volume attributes into pod spec; agent `rotateCertificates` work is a parallel tracked effort ([PD+Scope](https://docs.google.com/document/d/1tAuFq3x9Fapv55ZG0jQbAERld2tQtLiso6HiM1xZwLo/edit?tab=t.0)) not blocking MCK; main MCK-side cost is PEM combination logic and SERVER-109921 workaround |
| **Architectural complexity** | **Low** — no new K8s components; builds on existing hash-based PEM secret patterns; proven approach (ECK uses uber-secret pattern) | **Medium** — adds sidecar container to every pod; `emptyDir` lifecycle to manage; extra container image to maintain | **High** — new critical infrastructure component (HA webhook in every cluster) adds an additional failure domain tightly coupled to pod scheduling; most moving parts of all options | **Low (operator-side)** — once agent and SERVER-109921 dependencies land, operator simply adds CSI volume attributes to pod spec with no Secret lifecycle to manage; cleanest long-term architecture |
| **Recommendation** | **Recommended short-term** — lowest risk, no new infrastructure, ships independently of other teams | Alternative to Option 1 with marginally better in-pod isolation; higher cost for limited gain | Not recommended as primary path — operational burden of HA webhook in every cluster outweighs benefits vs. Options 1 and 4 | **Recommended long-term** — architecturally simplest, no certs in etcd, no mongod restarts on renewal, cleanest multi-cluster story; dependent on agent team work already in progress ([PD+Scope](https://docs.google.com/document/d/1tAuFq3x9Fapv55ZG0jQbAERld2tQtLiso6HiM1xZwLo/edit?tab=t.0)) |

---

## Summary Ratings

| Dimension | 1: Uber Secret | 2: Uber Secret + Sidecar | 3: Admission Webhook | 4: CSI Driver + Agent |
|-----------|:--------------:|:------------------------:|:--------------------:|:---------------------:|
| Key isolation | Low | Medium | High | High |
| Vault support | Medium | Medium | High | Medium |
| Production risk | Low | Low-Medium | Medium-High | Medium |
| Debug ease | Easy | Moderate | Moderate | Hard |
| Maintenance burden | Low | Medium | High | Medium → Low |
| Dev cost | Medium | Medium-High | High | Medium |
| Overall complexity | Low | Medium | High | Low (operator-side) |
