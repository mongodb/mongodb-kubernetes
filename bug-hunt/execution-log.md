# VM → Kubernetes Migration Bug Hunt — Execution Log

**Date:** 2026-07-17
**Branch:** `vm-migration-feature-branch` (commit `5dc66aefc`)
**Plugin:** S3-downloaded `kubectl-mongodb` (Build: `5dc66aefc6c44c5800df1b1ea41036378bf07429, 2026-07-16T13:46:34Z`)
**OM:** `http://ec2-52-29-62-219.eu-central-1.compute.amazonaws.com:30880`
**Project:** `migration` (`6a5a3a7cb8444f7b5da85100`)
**Org:** `6a59eeeeb8444f7b5d9217d7`
**Namespace:** `bughunt-nam`
**Kubeconfig:** `/var/folders/p4/962z_fnj1hx24gmwfs5qwgxm0000gn/T/opencode/bughunt.kubeconfig` (context `vm-migration-bughunt`)

---

## Step 0 — Environment Setup

### 0.1 Kubeconfig

**Input:**
- User provided EKS kubeconfig with `bughunt-admin` service account token.
- Saved to `/var/folders/p4/962z_fnj1hx24gmwfs5qwgxm0000gn/T/opencode/bughunt.kubeconfig` with `chmod 600`.
- First attempt with a different token failed (401: "server has asked for the client to provide credentials"). User provided a fresh kubeconfig that worked.

**Outcome:**
- `kubectl cluster-info` → control plane reachable at `https://0199E5E356FDB998F28417A53E5226E5.gr7.eu-central-1.eks.amazonaws.com`
- Namespace `bughunt-nam` confirmed Active.

### 0.2 Plugin Download

**Input:**
```bash
curl -sL "<S3 presigned URL from Google Doc>" -o kubectl-mongodb && chmod +x kubectl-mongodb
```

**Outcome:**
- Binary: Mach-O 64-bit executable arm64
- `--help` shows Build: `5dc66aefc6c44c5800df1b1ea41036378bf07429, 2026-07-16T13:46:34Z`
- `migrate-to-mck` subcommand available.
- Stored at `/var/folders/p4/962z_fnj1hx24gmwfs5qwgxm0000gn/T/opencode/kubectl-mongodb`

### 0.3 Initial Cluster Inventory

**Input:**
```bash
kubectl -n bughunt-nam get pods,sts,svc,cm,secrets,mdb,mongodbuser,deploy
```

**Outcome:**
- 5 `vm-mongodb` pods (Running, ~123m old)
- `vm-mongodb` StatefulSet: 5/5 ready
- `vm-mongodb` Service: ClusterIP None, port 27017
- ConfigMap `my-project`: `baseUrl=http://ec2-52-29-62-219...:30880`, `orgId=6a59eeeeb8444f7b5d9217d7`, `projectName=migration`
- Secret `my-credentials`: keys `publicKey`, `privateKey`
- Secret `vm-mongodb-cert`: keys `ca.crt`, `tls-combined.pem`, `tls.crt`, `tls.key`
- Secret `vm-agent-key`: Opaque
- Operator deployment: `mongodb-kubernetes-operator` 1/1 Running
- No MongoDB or MongoDBUser CRs

### 0.4 OM API Access

**Input:**
```bash
PUB_KEY=$(kubectl -n bughunt-nam get secret my-credentials -o jsonpath='{.data.publicKey}' | base64 -d)
PRIV_KEY=$(kubectl -n bughunt-nam get secret my-credentials -o jsonpath='{.data.privateKey}' | base64 -d)
curl --digest -u "$PUB_KEY:$PRIV_KEY" "$BASE_URL/api/public/v1.0/orgs/$ORG_ID/groups"
```

**Outcome:**
- Basic auth → 401. OM uses Digest auth (`WWW-Authenticate: Digest realm="MMS Public API"`).
- Digest auth → 200. One project found: `id=6a5a3a7cb8444f7b5da85100`, name=`migration`.
- Note: Project ID differs from the URL (`6a59eef0...` was old, current is `6a5a3a7c...`).

### 0.5 VM Pod Inspection

**Input:**
```bash
kubectl -n bughunt-nam get pod vm-mongodb-0 -o jsonpath='{.spec.containers[*].name}'
kubectl -n bughunt-nam exec vm-mongodb-0 -- ls /var/lib/mongodb-mms-automation/
```

**Outcome:**
- Single container: `mongodb-agent` (image `quay.io/mongodb/mongodb-agent:108.0.25.9029-1`)
- No `mongod` or `mongosh` in PATH — agent downloads MongoDB binaries when automation config is pushed.
- Volumes: `mongodb-data` (emptyDir 5Gi, mounted at `/data`), `mongodb-certs` (secret `vm-mongodb-cert`, mounted at `/etc/mongodb/certs` and subPath `ca.crt` at `/mongodb-automation/tls/ca/ca-pem`)
- Agent env: `MMS_GROUP_ID=6a5a3a7cb8444f7b5da85100`, `MMS_BASE_URL=http://om-svc.mck-om.svc.cluster.local:8080`

---

## Step 1 — First Deployment Attempt (Sharded Cluster, User-Created)

### 1.1 Inspect User's Deployment

**Input:**
```bash
curl --digest -u "$PUB_KEY:$PRIV_KEY" "$BASE_URL/api/public/v1.0/groups/$PROJ_ID/automationConfig"
```

**Outcome:**
- 3 processes: `myCluster_myShard_0_1`, `myCluster_myShard_1_2`, `myCluster_mongos_3`
- 2 replica sets: `myShard_0` (1 member), `myShard_1` (1 member)
- Sharding config: `configServerReplica: "myShard_0"`, shard `_id: "config"` uses `rs: "myShard_0"` — **embedded config server**
- Auth: disabled (no mechanisms), TLS: `clientCertificateMode: OPTIONAL`
- 1 user `root` with no mechanisms (likely agent auto-user)

### 1.2 Generate CR — Blocked by Embedded Config Server

**Input:**
```bash
./bin/kubectl-mongodb migrate-to-mck mongodb --config-map-name my-project --secret-name my-credentials --namespace bughunt-nam -o mongodb-cr.yaml
```
(Using locally-built plugin initially, later switched to S3 plugin.)

**Outcome:**
```
[ERROR] Sharded cluster "myCluster" has shard "config" backed by replica set "myShard_0" which is also the config server (configServerReplica points to a shard replica set). The operator does not support an embedded config server. Move the config server to a dedicated replica set before migrating.
Error: validation failed: 1 error(s) found.
```
- CR generation failed. This is correct validation — the operator genuinely doesn't support embedded config servers.
- User removed all deployments via OM UI.

---

## Step 2 — Create Replica Set via OM API

### 2.1 Push Automation Config

**Input:**
PUT to `/api/public/v1.0/groups/$PROJ_ID/automationConfig` with a 3-member replica set `rs0`, SCRAM-SHA-256, TLS `requireTLS`, MongoDB 7.0.12.

Multiple iterations to fix API validation errors:
1. `Invalid attribute port specified` → removed top-level `port` from processes (port lives in `args2_6.net.port`)
2. `Invalid attribute authorization specified` → removed nested `authorization` object (was inside `auth`)
3. `The required attribute authSchemaVersion was not specified` → added `authSchemaVersion` as per-process field (not in auth)
4. `Invalid attribute authSchemaVersion specified` → moved `authSchemaVersion` from `auth` to each process object
5. `auth.autoAuthMechanisms may not contain SCRAM-SHA-256 unless all processes have featureCompatibilityVersion >= 4.0` → added `featureCompatibilityVersion: "7.0"` to each process
6. `The required attribute auth.usersWanted.db was not specified` → renamed `database` to `db` in usersWanted entries (JSON field is `db`, not `database`)
7. `SSL is not enabled for this deployment, but process rs0_0 has sslMode set` → added `tls.CAFilePath` to top-level `tls` section
8. Also renamed `password` to `initPwd` in usersWanted entries.

Final accepted config: HTTP 200.

**Outcome:**
- 3 processes: `rs0_0` (vm-mongodb-0), `rs0_1` (vm-mongodb-1), `rs0_2` (vm-mongodb-2)
- 1 replica set: `rs0` with 3 members
- Auth: `disabled=false`, `autoUser=mms-automation`, `deploymentAuthMechanisms=["SCRAM-SHA-256"]`, `keyfile=/data/keyfile`, `keyfileWindows=C:\data\keyfile`
- 2 users: `mms-automation` (admin roles), `app-user` (readWriteAnyDatabase)
- TLS: `requireTLS`, `CAFilePath=/mongodb-automation/tls/ca/ca-pem`, `clientCertificateMode=OPTIONAL`

### 2.2 Fix Keyfile Paths for Plugin Validation

**Input:**
- First plugin run rejected: `auth.keyFile "/data/keyfile" differs from the operator default "/var/lib/mongodb-mms-automation/keyfile"`
- GET current AC, change `keyfile` → `/var/lib/mongodb-mms-automation/keyfile`, `keyfileWindows` → `%SystemDrive%\MMSAutomation\versions\keyfile`, PUT back.

**Outcome:** HTTP 200. Agents reconciled to goal state.

### 2.3 Verify Deployment

**Input:**
```bash
kubectl -n bughunt-nam logs vm-mongodb-0 --tail=20
```

**Outcome:**
- Agent logs: `All 1 Mongo processes are in goal state.`
- Mongosh found at `/var/lib/mongodb-mms-automation/mongosh-linux-x86_64-2.8.1/bin/mongosh`

### 2.4 Insert Sentinel Data

**Input:**
```bash
kubectl exec vm-mongodb-0 -- mongosh --host rs0/vm-mongodb-0...:27017 --tls --tlsCAFile /mongodb-automation/tls/ca/ca-pem --tlsAllowInvalidHostnames -u "$MDB_USER" -p "$MDB_PASSWORD" --authenticationDatabase admin --eval 'db.getSiblingDB("bughunt").sentinel.insertOne({ts:new Date(), where:"vm"})'
```

**Outcome:**
- Insert acknowledged: `ObjectId('6a5a3da39cc53857fafdca27')`
- Verified as `app-user`: `[{_id: ObjectId('6a5a3da3...'), ts: ISODate('2026-07-17T14:35:15.138Z'), where: 'vm' }]`
- RS status: vm-mongodb-0 = PRIMARY, vm-mongodb-1 = SECONDARY, vm-mongodb-2 = SECONDARY

---

## Step 3 — A1: Raw Generated CR Admission Test

### 3.1 Generate CR with S3 Plugin

**Input:**
```bash
/var/folders/.../kubectl-mongodb migrate-to-mck mongodb \
  --config-map-name my-project --secret-name my-credentials \
  --namespace bughunt-nam --certs-secret-prefix mdb \
  -o mongodb-cr.yaml
```

**Outcome:**
- Warnings: TLS CA config notice, `additionalMongodConfig` from `rs0_0` notice.
- CR generated successfully.
- Version annotation: `mongodb.com/migrate-tool-version: 5dc66aef` (matches operator)
- **No `members` field in CR** (zero, omitted by `omitempty`) — this was the attack surface.
- CR has `mongodb.com/migration-dry-run: "true"` annotation by default.

### 3.2 Earlier Version Mismatch (noted)

**Input:** Initially used locally-built plugin with `OperatorVersion=1.8.0`.

**Outcome:**
```
admission webhook "mdbpolicy.mongodb.com" denied the request: The resource was generated with import tool version 1.8.0. Operator is on version 5dc66aef.
```
- **A15 confirmed:** Version mismatch gate works as expected. Switched to S3 plugin which stamps `5dc66aef`.

### 3.3 Apply Raw CR

**Input:**
```bash
kubectl -n bughunt-nam apply -f mongodb-cr.yaml
```

**Outcome:**
```
mongodb.mongodb.com/rs0 created
```
- **A1 RESULT: Admission ACCEPTED the raw CR with `members: 0` (omitted).**
- This **contradicts** the analyst's prediction that `replicasetMemberIsSpecified` would reject it.
- The operator immediately started dry-run connectivity validation (because the CR has `migration-dry-run: "true"`).
- Operator log: `desiredReplicas:0`, `isScaling:false` — operator handles `members=0` with `externalMembers` present.

### 3.4 Dry-Run Connectivity — First Attempt (No TLS Resources)

**Outcome:**
- Phase: `ConnectivityValidation` → `Failed`
- Condition: `NetworkConnectivityVerification: False - Network connectivity failed`
- Job `rs0-connectivity-check` failed.
- Job logs: `Failed to read mongod CA file: open /mongodb-automation/tls/ca/ca-pem: no such file or directory`
- Root cause: `rs0-ca` ConfigMap and `mdb-rs0-cert` Secret did not exist yet.

### 3.5 Create TLS Resources

**Input:**
```bash
# Create rs0-ca ConfigMap from vm-mongodb-cert CA
kubectl -n bughunt-nam get secret vm-mongodb-cert -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
kubectl -n bughunt-nam create configmap rs0-ca --from-file=ca-pem=ca.crt --from-file=mms-ca.crt=ca.crt

# Create mdb-rs0-cert Secret (copy from vm-mongodb-cert)
kubectl -n bughunt-nam get secret vm-mongodb-cert -o json | <rename to mdb-rs0-cert> | kubectl apply -f -
```

**Outcome:**
- `configmap/rs0-ca created` (keys: `ca-pem`, `mms-ca.crt`)
- `secret/mdb-rs0-cert created` (keys: `ca.crt`, `tls-combined.pem`, `tls.crt`, `tls.key`)

### 3.6 Dry-Run Connectivity — Retry

**Input:**
- Deleted failed job, removed and re-added `migration-dry-run` annotation to trigger reconcile.

**Outcome:**
- Status remained `Failed` — the operator did not re-create the connectivity job immediately.
- Execution was interrupted by user request to document progress.

**Status:** In progress — need to trigger a fresh reconcile to retry dry-run with TLS resources now in place.

---

## Step 4 — Bounded Recovery (Namespace-Local CA Issuer)

**Goal:** Recover cert issuance for namespace `bughunt-nam` only, using a namespace-local cert-manager CA Issuer backed by the existing CA pair at `/tmp/bughunt-certs/{ca.crt,ca.key}` (CN=bughunt-ca). No resource outside `bughunt-nam` may be mutated. No private-key bytes written to repo or logs.

### 4.1 Root Cause — Shared Issuer Corruption

**Input:**
```bash
kubectl -n cert-manager get certificate vm-bughunt-ca -o jsonpath='{.status.conditions}'
kubectl -n cert-manager get secret vm-bughunt-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | openssl x509 -noout -subject -fingerprint -sha1
kubectl -n cert-manager get secret vm-bughunt-ca -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -subject -fingerprint -sha1
```

**Outcome:**
- The shared `vm-bughunt-ca` Secret in `cert-manager` ns is internally inconsistent:
  - `ca.crt` key → CN=vm-bughunt-ca, sha1 `3A:CF:0A:60:68:D6:5D:D1:EB:3C:1F:8C:C6:8B:7D:A0:97:D2:79:F8` (original self-signed CA).
  - `tls.crt` key → CN=bughunt-ca, sha1 `60:7E:0F:17:20:CE:1D:60:8C:D7:C6:F6:E5:36:1C:42:DF:C4:19:50` (matches `/tmp/bughunt-certs/ca.crt`).
- The `tls.crt`/`tls.key` pair was overwritten with the `/tmp/bughunt-certs` CA pair, breaking the self-signed Certificate's key-spec tracking.
- cert-manager ns Certificate `vm-bughunt-ca`: `Ready=False`, reason `SecretMismatch`, "Existing private key is not up to date for spec: [spec.privateKey.algorithm]".
- ClusterIssuer `vm-bughunt-ca`: `Ready=True` ("Signing CA verified") but actual signing fails — "certificate chain is malformed or broken".
- This is the accidental shared issuer corruption. It remains broken for future issuance (see 4.8).

### 4.2 Pre-Recovery State of bughunt-nam

**Input:**
```bash
kubectl -n bughunt-nam get certificate vm-mongodb-cert -o jsonpath='{.status.conditions}'
kubectl -n bughunt-nam get secret vm-mongodb-cert
kubectl -n bughunt-nam get certificaterequests -o wide
```

**Outcome:**
- Certificate `vm-mongodb-cert`: `Ready=False` (reason `DoesNotExist`, "Issuing certificate as Secret does not exist"); `Issuing=False` (reason `Failed`, "Error signing certificate: certificate chain is malformed or broken"); `failedIssuanceAttempts: 1`, revision 6.
- Secret `vm-mongodb-cert`: NotFound (deleted).
- CertificateRequest `vm-mongodb-cert-7`: `READY=False`, "Error signing certificate: certificate chain is malformed or broken" (stale failed request against the broken shared issuer).
- Certificate spec: CN=`vm-mongodb.bughunt-nam.svc.cluster.local`, 6 DNS names (`vm-mongodb` + `vm-mongodb-0..4`), `additionalOutputFormats: [CombinedPEM]`, `issuerRef: {kind: ClusterIssuer, name: vm-bughunt-ca}`, `secretName: vm-mongodb-cert`.

### 4.3 CA Pair Validation

**Input:**
```bash
openssl x509 -in /tmp/bughunt-certs/ca.crt -noout -text   # CA cert is non-secret
openssl x509 -in /tmp/bughunt-certs/ca.crt -noout -modulus | openssl md5
openssl rsa -in /tmp/bughunt-certs/ca.key -noout -modulus | openssl md5   # modulus only, no key bytes
```

**Outcome:**
- CA cert: CN=bughunt-ca, self-signed, `X509v3 Basic Constraints: critical, CA:TRUE`, RSA, validity 2026-07-17 → 2027-07-17.
- `ca.crt` modulus md5 == `ca.key` modulus md5 → valid key pair (modulus digests compared; no private-key bytes displayed).

### 4.4 Create Namespace-Local CA Secret + Issuer

**Input:**
```bash
kubectl -n bughunt-nam create secret tls bughunt-ca-key-pair \
  --cert=/tmp/bughunt-certs/ca.crt --key=/tmp/bughunt-certs/ca.key
kubectl -n bughunt-nam apply -f bug-hunt/artifacts/bughunt-ca-issuer.yaml   # Issuer spec.ca.secretName=bughunt-ca-key-pair
kubectl -n bughunt-nam wait issuer/bughunt-ca --for=condition=ready=True --timeout=30s
```

**Outcome:**
- `secret/bughunt-ca-key-pair created` (type `kubernetes.io/tls`, keys `tls.crt`, `tls.key`). Private key loaded directly from `/tmp`; no key bytes written to repo or logs.
- `issuer.cert-manager.io/bughunt-ca created` (namespace-local CA Issuer). Manifest saved to `bug-hunt/artifacts/bughunt-ca-issuer.yaml` (non-secret).
- Issuer `Ready=True`, reason `KeyPairVerified`, "Signing CA verified".

### 4.5 Patch Certificate issuerRef + Force Clean Issuance

**Input:**
```bash
kubectl -n bughunt-nam patch certificate vm-mongodb-cert --type=merge \
  -p '{"spec":{"issuerRef":{"kind":"Issuer","name":"bughunt-ca"}}}'
kubectl -n bughunt-nam delete certificaterequest vm-mongodb-cert-7 --ignore-not-found
# poll certificate Ready until True
```

**Outcome:**
- Certificate patched: `issuerRef` → `{kind: Issuer, name: bughunt-ca}`; generation 1→2; resourceVersion 477207→492061.
- Stale failed CR `vm-mongodb-cert-7` deleted.
- cert-manager re-issued within ~5s: Certificate `Ready=True`, reason `Ready`, "Certificate is up to date and has not expired".
- Secret `vm-mongodb-cert` recreated with 4 keys: `ca.crt`, `tls-combined.pem`, `tls.crt`, `tls.key`.

### 4.6 Pre-Restart Certificate Verification

**Input:**
```bash
kubectl -n bughunt-nam get secret vm-mongodb-cert -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/issued-tls.crt
kubectl -n bughunt-nam get secret vm-mongodb-cert -o jsonpath='{.data.ca\.crt}'  | base64 -d > /tmp/issued-ca.crt
openssl x509 -in /tmp/issued-tls.crt -noout -subject -issuer
openssl x509 -in /tmp/issued-tls.crt -noout -text | grep -A8 'Subject Alternative Name'
openssl x509 -in /tmp/issued-ca.crt  -noout -subject -fingerprint -sha1
openssl verify -CAfile /tmp/issued-ca.crt /tmp/issued-tls.crt
# leaf cert/key modulus compare (no key bytes shown)
```

**Outcome (all PASS):**
- Leaf subject: CN=vm-mongodb.bughunt-nam.svc.cluster.local.
- Leaf issuer: CN=bughunt-ca.
- DNS SANs (all 6 present): `vm-mongodb.bughunt-nam.svc.cluster.local`, `vm-mongodb-0..4.vm-mongodb.bughunt-nam.svc.cluster.local`.
- CA subject: CN=bughunt-ca; issued `ca.crt` sha1 `60:7E:0F:17:20:CE:1D:60:8C:D7:C6:F6:E5:36:1C:42:DF:C4:19:50` == `/tmp/bughunt-certs/ca.crt` sha1 → **CA key match confirmed**.
- `openssl verify -CAfile <issued-ca> <issued-leaf>` → `OK`.
- Leaf `tls.crt` modulus md5 == Secret `tls.key` modulus md5 → valid leaf key pair.

### 4.7 Rolling Restart of bughunt-nam VM Pods

**Input:**
```bash
kubectl -n bughunt-nam rollout restart sts/vm-mongodb
kubectl -n bughunt-nam rollout status sts/vm-mongodb --timeout=180s
```

**Outcome:**
- StatefulSet rolling restart completed (partitioned, one pod at a time, 5/5 updated).
- All 5 pods recreated with new UIDs and start times ~16:31:34–16:31:39Z; all `Running 1/1`.
- Only `sts/vm-mongodb` (bughunt-nam) restarted; `rs0-0` K8s pod and operator untouched.

### 4.8 Post-Restart Verification

> **Point-in-time snapshot.** The RS status, OM membership, and MongoDB CR phase below were observed immediately after the Step 4.7 rolling restart (~16:31-16:39Z on 2026-07-17). They are a snapshot only and must not be read as the current state. The exact 5-process OM membership here is stale relative to the later read-only observation in 4.11 (where the CR is `Running` and rs0-1/rs0-2 exist).

**Mounted certs (vm-mongodb-0):**
- `/mongodb-automation/tls/ca/ca-pem` (subPath `ca.crt`): subject=CN=bughunt-ca, sha1 `60:7E:…` (matches issued CA).
- `/etc/mongodb/certs/tls.crt`: subject=CN=vm-mongodb.bughunt-nam.svc.cluster.local, issuer=CN=bughunt-ca, all 6 DNS SANs present.

**Agent logs (vm-mongodb-0):**
- "All 1 Mongo processes are in goal state. Monitoring in goal state, Backup in goal state."
- "In Goal State for clusterConfig version: 11."
- TLS auth to `vm-mongodb-0…:27017` succeeded (`tls=true`).

**rs0-0 (K8s pod):** 1/1 Running; TLS auth to `rs0-0.rs0-svc…:27017` succeeded.

**Replica set status (point-in-time, queried via mongosh over TLS as `mms-automation`, SCRAM-SHA-256, ~16:31-16:39Z):**
- `rs0`, myState=1.
- vm-mongodb-0 = PRIMARY (health=1).
- vm-mongodb-1 = SECONDARY (health=1).
- vm-mongodb-2 = SECONDARY (health=1).
- rs0-0.rs0-svc.bughunt-nam.svc.cluster.local = SECONDARY (health=1).
- Only rs0-0 was a K8s RS member at this snapshot; rs0-1/rs0-2 did not yet exist (see 4.11).

**MongoDB CR `rs0` (point-in-time, ~16:31-16:39Z):** phase `Failed` (pre-existing, unchanged by this recovery); condition `NetworkConnectivityVerification=True` ("All external members reachable and authenticated"). Reported as-is. This `Failed` phase is stale by the later observation in 4.11 (CR is `Running` there).

**OM membership (point-in-time, automationStatus API, ~16:31-16:39Z):**
- `goalVersion: 12`.
- All 5 processes (`rs0_0`, `rs0_1`, `rs0_2`, `k8s/bughunt-nam/rs0-0`, `k8s/bughunt-nam/rs0-1`) at `lastGoalVersionAchieved: 12`, `errorCode: 0`, empty `plan` — all agents in goal state.
- This exact 5-process membership is a snapshot and must not be treated as current; OM was not re-queried for the later observation in 4.11.

**Sentinel (queried, not assumed):**
- `db.getSiblingDB("bughunt").sentinel.find().toArray()` → count **0**, result `[]`.
- **Sentinel does NOT survive.** The document inserted at Step 2.4 (`{ts, where:"vm"}`) is absent.
- Loss attribution (cautious): the exact moment of loss was not directly observed and is **unproven**. The VM pods mount `mongodb-data` as `emptyDir` (non-persistent, 5Gi), so any pod recreation wipes that volume. Two plausible direct causes exist: (a) the earlier A1b events — deleting a migration CR with `migration-dry-run=true` wiped the OM automation config (AC version jumped 1→5), shut down mongod, and was followed by pod restarts; and (b) the Step 4.7 rolling restart of `sts/vm-mongodb` performed during this bounded recovery, which recreated all 5 emptyDir-backed vm-mongodb pods (~16:31Z) and thereby wiped their data volumes. (b) is a plausible direct cause occurring within this recovery itself; the loss cannot be attributed solely to earlier events, and the relative contribution of (a) vs (b) is not established.

**Shared issuer remains broken:**
- cert-manager ns Certificate `vm-bughunt-ca`: still `Ready=False`, reason `SecretMismatch`, "Existing private key is not up to date for spec: [spec.privateKey.algorithm]".
- cert-manager ns Secret `vm-bughunt-ca`: resourceVersion unchanged (477178) — not touched by this recovery.
- The shared ClusterIssuer `vm-bughunt-ca` is broken for future issuance. Other namespaces' existing Certificates remain `Ready=True` (served from cached Secrets); new issuance or renewal through the shared issuer will fail.

### 4.9 Cross-Namespace Immutability Evidence

**Input:**
```bash
# Before any change: capture rv/uid of all ClusterIssuers, Certificates, Secrets (excluding bughunt-nam), Issuers.
# After all changes: re-capture identically.
diff /tmp/bughunt-baseline-before.txt /tmp/bughunt-baseline-after.txt
```

**Outcome:**
- Baseline files contain only non-secret metadata (resourceVersion, UID, names) — no secret data, no private keys.
- Diff shows exactly two changes, both in `bughunt-nam`:
  1. `bughunt-nam/vm-mongodb-cert` Certificate: rv 477207→492061 (same UID — the issuerRef patch).
  2. `bughunt-nam/bughunt-ca` Issuer: new (UID b8ea3056…).
- Zero resources outside `bughunt-nam` changed: ClusterIssuers (rv unchanged), cert-manager ns Certificate + Secret (rv 477180 / 477178 unchanged), all other namespaces' Certificates and Secrets (rv/uid unchanged), no Issuers existed before or after outside bughunt-nam.

### 4.10 Exact Mutations (all in bughunt-nam)

| Resource | Action |
|---|---|
| `secret/bughunt-ca-key-pair` | Created (CA key-pair from /tmp files; no key bytes in repo) |
| `issuer.cert-manager.io/bughunt-ca` | Created (namespace-local CA Issuer) |
| `certificate.cert-manager.io/vm-mongodb-cert` | Patched (issuerRef → Issuer/bughunt-ca) |
| `certificaterequest/vm-mongodb-cert-7` | Deleted (stale failed) |
| `secret/vm-mongodb-cert` | Recreated by cert-manager (was absent) |
| `sts/vm-mongodb` pods (5) | Rolling restart |
| `bug-hunt/artifacts/bughunt-ca-issuer.yaml` | Written (non-secret manifest) |

**Status:** Recovery complete and verified. Shared issuer remains broken for future issuance. Sentinel does not survive (count=0, queried); loss point unproven — the Step 4.7 rolling restart during this recovery is a plausible direct cause (see 4.8 attribution). A later read-only observation (4.11) shows the CR has since reached `Running` and rs0-1/rs0-2 exist; it must not be read back into the Step 4.8 snapshot.

### 4.11 Later Read-Only Observation (no mutations)

**Time:** 2026-07-17T16:39:18Z. Read-only `kubectl get`/`jsonpath` only — no resource was created, patched, or deleted in this pass.

**Observed:**
- MongoDB CR `rs0`: phase **`Running`**, version `7.0.12`, type `ReplicaSet`.
  - Condition `NetworkConnectivityVerification=True`, reason `NetworkValidationPassed`, "All external members reachable and authenticated" (lastTransitionTime `2026-07-17T15:07:45Z`).
- StatefulSet `rs0`: 3/3 ready.
- K8s pods (all Running): `rs0-0` (startTime `2026-07-17T15:22:37Z`), `rs0-1` (startTime `2026-07-17T16:33:02Z`), `rs0-2` (startTime `2026-07-17T16:34:08Z`). **rs0-1 and rs0-2 now exist** (absent at the Step 4.8 snapshot).
- vm-mongodb-0..4: all Running (startTime `2026-07-17T16:31:34-39Z`, coincident with the Step 4.7 rolling restart; no further vm-mongodb restart observed since).

**Not claimed (no evidence):** Why or exactly when the CR transitioned from `Failed` (Step 4.8 snapshot) to `Running`, or what caused rs0-1/rs0-2 to appear, is not established by this read-only pass. The OM automationStatus was not re-queried in this pass, so the current OM process membership is unknown; the Step 4.8 5-process / goalVersion-12 snapshot must not be read as current.

---

## Step 5 — A7: Invalid Voting Configuration (>7 Voters), Admission-Only Dry-Run

**Time:** 2026-07-18T09:11:27Z – 09:13:14Z.
**Kubeconfig:** `bug-hunt/artifacts/bughunt.kubeconfig` (context `vm-migration-bughunt`). Note: Step 0.1 referenced a `/var/folders/...` path; the canonical copy now lives at `bug-hunt/artifacts/bughunt.kubeconfig` and was used here.
**Method:** `kubectl apply --dry-run=server` (server-side dry-run = admission webhooks run, nothing persisted). No resource was created, patched, or deleted. The live CR `rs0` was not mutated (before/after fingerprint proof below).

### 5.1 Source Analysis (before testing)

**Admission validators for a MongoDB ReplicaSet CR** (`api/mongodb/v1/mdb/mongodb_validation.go`, `RunValidations`):
- `horizonsMustEqualMembers`, `horizonDomainNamesMustBeValid`, `additionalMongodConfig`, `replicasetMemberIsSpecified` (only rejects `Members==0` for ReplicaSet).
- `CommonValidators`: TLS/auth/FCV checks. None count voting members.
- Update validators: `resourceTypeImmutable`, `noTopologyMigration`, `noSimultaneousTLSDisablingAndScaling`.
- **No admission validator checks >7 voting members.** Reconcile-time enforcement (per PR800 source `/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`, the `5dc66aefc` vm-migration-feature-branch tree): for migration CRs (externalMembers present) the 7-voter limit is enforced by `validateACForMigration` (`controllers/operator/common_controller.go:459-494`, called from `mongodbreplicaset_controller.go:237`) → `validateVotingLimitRS` (`:590-594`) → `validateVotingLimit` (`:556-572`), which returns `workflow.Failed(...)` with a detailed error listing every voting member and which to make non-voting when `total > MaxVotingMembers (7)` — it **fails the reconcile, it does NOT silently coerce**. `Deployment.limitVotingMembers` (`controllers/om/deployment.go:1310-1325`) explicitly **no-ops when externalMembers are present** (`if len(externalMembers) > 0 { return }`, `:1311-1313`); its doc comment (`:1305-1309`) and the call-site comment (`:183-184`) state the no-op is intentional because the operator does not own external members' votes, and `validateACForMigration` is the user-facing failure path. `limitVotingMembers` only auto-zeroes excess voters for pure-K8s deployments (no external members). **Retraction:** the earlier "silently coerces" claim was read from the master source tree's `limitVotingMembers` (`controllers/om/deployment.go:1177-1190`, signature `limitVotingMembers(rsName string)` with no externalMembers guard) and incorrectly applied to the migration case; it is false for migration and retracted.

**Migration change-type validator:** Not present in the checked-out source tree. The running operator image (`quay.io/mongodb/staging/mongodb-kubernetes:5dc66aef`, a staging build of `vm-migration-feature-branch`) emits `"only one migration change type is allowed per update: adding Kubernetes members, removing external members, or updating member votes/priority"`. This validator compares old vs new spec on UPDATE and rejects combined change types. It is not exercised on CREATE (no old object).

### 5.2 Before Fingerprint (live CR `rs0`)

**Time:** 2026-07-18T09:11:27Z.
```
resourceVersion: 493314
generation: 4
uid: 5466e872-9a9a-432b-b617-d86c94eac4d9
phase: Running
spec.members: 3
spec.externalMembers.count: 3
spec.memberConfig: [{"priority":"0","votes":0},{"priority":"0","votes":0},{"priority":"0","votes":0}]
spec-sha256: 3fc5c2736eb6b65fac8e8b2f2d9728762c65a3b43be8de23166fddd10a7b2cf2
```
Live state: 3 external VM members (default votes=1 → 3 voters) + 3 K8s members (votes:0 → 0 voters) = **3 voters**. CR `Running`.

### 5.3 Candidate Manifests

All candidates are based on the live CR spec; only `spec.members`, `spec.memberConfig`, and (for the CREATE test) `metadata.name` differ. Full manifests saved to `bug-hunt/artifacts/a7-candidate*.yaml` (non-secret).

**Voting math for >7:** 3 external (default votes=1) + 5 K8s (votes:1) = **8 voters > 7**.

| File | Path | members | memberConfig votes | External voters | K8s voters | Total |
|---|---|---|---|---|---|---|
| `a7-candidate.yaml` | UPDATE `rs0` | 5 | 5×1 | 3 | 5 | 8 |
| `a7-candidate-create.yaml` | CREATE `rs0-a7test` | 5 | 5×1 | 3 | 5 | 8 |
| `a7-candidate-votes-only.yaml` | UPDATE `rs0` | 3 | 3×1 | 3 | 3 | 6 |
| `a7-candidate-add-members-only.yaml` | UPDATE `rs0` | 8 | 3×0 | 3 | 0 | 3 |

### 5.4 Dry-Run Results (admission-only, nothing persisted)

**TEST 0 — UPDATE `rs0`, combined changes (add members + change votes), 8 voters:**
```
$ kubectl -n bughunt-nam apply --dry-run=server -f a7-candidate.yaml
Error from server (Forbidden): admission webhook "mdbpolicy.mongodb.com" denied the request:
only one migration change type is allowed per update: adding Kubernetes members, removing
external members, or updating member votes/priority
```
HTTP 403 Forbidden. Denied by the migration change-type validator (not by a >7-voter check). The PATCH request URL confirmed `dryRun=All`: `PATCH .../mongodb/rs0?dryRun=All&fieldManager=kubectl-client-side-apply&fieldValidation=Strict`.

**TEST 1 — CREATE `rs0-a7test`, 8 voters > 7 (no old object → migration change-type validator N/A):**
```
$ kubectl -n bughunt-nam apply --dry-run=server -f a7-candidate-create.yaml
mongodb.mongodb.com/rs0-a7test created (server dry run)
```
Exit 0. **Admission ACCEPTED a CR with 8 voting members (>7).** No validator counts voters.

**TEST 2 — UPDATE `rs0`, single change type (votes only 0→1), 6 voters:**
```
$ kubectl -n bughunt-nam apply --dry-run=server -f a7-candidate-votes-only.yaml
mongodb.mongodb.com/rs0 configured (server dry run)
```
Exit 0. Single change type accepted. 6 voters (not >7) — documents that the current live state cannot reach >7 via a votes-only update.

**TEST 3 — UPDATE `rs0`, single change type (add K8s members 3→8, votes unchanged at 0), 3 voters:**
```
$ kubectl -n bughunt-nam apply --dry-run=server -f a7-candidate-add-members-only.yaml
mongodb.mongodb.com/rs0 configured (server dry run)
```
Exit 0. Single change type accepted. 3 voters (K8s all votes:0) — documents that adding members alone (without votes) cannot reach >7 from the current state.

### 5.5 After Fingerprint + Mutation Proof

**Time:** 2026-07-18T09:13:14Z.
```
resourceVersion: 493314
generation: 4
uid: 5466e872-9a9a-432b-b617-d86c94eac4d9
phase: Running
spec.members: 3
spec.externalMembers.count: 3
spec.memberConfig: [{"priority":"0","votes":0},{"priority":"0","votes":0},{"priority":"0","votes":0}]
spec-sha256: 3fc5c2736eb6b65fac8e8b2f2d9728762c65a3b43be8de23166fddd10a7b2cf2
```
- `rs0-a7test`: NotFound (CREATE dry-run did not persist).
- `kubectl -n bughunt-nam get mongodb` → only `rs0` exists.
- **BEFORE == AFTER** on resourceVersion, generation, uid, spec-sha256, phase, members, memberConfig, externalMembers. **No mutation.**

### 5.6 A7 Result

**Admission does NOT reject >7 voting members.** Verified via CREATE dry-run (TEST 1): a MongoDB ReplicaSet CR with 3 external voting members + 5 K8s voting members (8 total > 7) was accepted by the `mdbpolicy.mongodb.com` webhook. The admission validator set (`RunValidations`) contains no voter-count check.

**Migration change-type validator (UPDATE path):** The running operator rejects UPDATEs that combine "adding Kubernetes members" with "updating member votes/priority" (TEST 0). From the current live state (3 external voters + 3 non-voting K8s), this means >7 voters **cannot be reached in a single UPDATE** — reaching 8 voters requires both adding K8s members and granting them votes, which are two separate change types. Single-change-type UPDATEs are accepted (TEST 2, TEST 3) but neither alone exceeds 7 voters from this state.

**Reconcile-time behavior (source-proven per PR800, not runtime-observed):** What the operator does at reconcile time with a >7-voter CR was not exercised here (no CR was persisted). PR800 source inspection (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`) shows that for migration CRs (externalMembers present) the reconcile path calls `validateACForMigration` (`controllers/operator/common_controller.go:459`) → `validateVotingLimitRS` (`:590`) → `validateVotingLimit` (`:556`), which returns `workflow.Failed(...)` when total voters > `MaxVotingMembers` (7) — i.e., the reconcile **fails** with a detailed error, it does NOT silently coerce. `Deployment.limitVotingMembers` (`controllers/om/deployment.go:1310`) explicitly no-ops when `externalMembers` is non-empty (`:1311-1313`); silent vote-zeroing only occurs for pure-K8s deployments. The earlier "silently coerces" statement is retracted as false for the migration case. This is source-level proof of the failure path, not observed runtime behavior (no >7-voter CR was persisted to confirm `phase=Failed`).

**Artifacts written (non-secret):** `bug-hunt/artifacts/a7-candidate.yaml`, `a7-candidate-create.yaml`, `a7-candidate-votes-only.yaml`, `a7-candidate-add-members-only.yaml`.

### 5.7 Secondary Observation — TEST 3 `members`/`memberConfig` Length Mismatch (unverified hypothesis, NOT a confirmed bug)

**Observation (admission, verified):** TEST 3 (UPDATE `rs0`, single change type "add K8s members 3→8") used `spec.members: 8` but only **3** `spec.memberConfig` entries (all `votes: 0`). Admission accepted it (`rs0 configured (server dry run)`, exit 0) — the `mdbpolicy.mongodb.com` webhook does not reject a `members`/`memberConfig` length mismatch, and the migration change-type validator saw a single change type (3 voters, not >7).

**Reconcile-time behavior (partially source-inspected — labeled hypothesis, not confirmed):** Only the voting-limit path was inspected in PR800 source (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`):
- ReplicaSet path: `computePostReconcileVoting` (`controllers/operator/common_controller.go:690-719`) iterates `i` in `[0, mdb.Spec.Members)`; when `i >= len(mdb.Spec.GetMemberOptions())` it uses a zero-value `automationconfig.MemberOptions{}` (`:703-706`), whose `GetVotes()` returns 0 → those positions are non-voting (`:707-709`).
- Sharded path: `votingPositionsFromConfig` (`:575-587`) does the same defaulting (`:578-582`).
- So for TEST 3's exact spec (members=8, 3 memberConfig entries all votes:0, 3 external voters), reconcile would compute 0 K8s voting positions + 3 external = 3 ≤ 7 → `validateVotingLimit` passes. **This spec is NOT a >7-voter vector.**

**Why this stays a hypothesis, not a confirmed bug:** only the voting-limit code path was source-inspected. Whether reconcile handles a `members`/`memberConfig` length mismatch safely in **all** respects (StatefulSet replica count, per-pod memberConfig application, AC merge behavior for the 5 unspecified members) is NOT proven by this inspection, and no CR with this mismatch was persisted (TEST 3 was a server-side dry-run; nothing was applied). Labeled as a reconcile-time validation-gap hypothesis. To confirm or clear it, one would persist a CR with `members=8` + 3 `memberConfig` entries and observe the full reconcile, or inspect the merge/StatefulSet paths beyond the voting-limit check.

---

## Step 6 — A13 & A14: Unguarded Panic Paths (Code Inspection + Safe Unit Repro)

**Time:** 2026-07-18.
**Method:** Static source inspection of the PR800 tree (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`, the `5dc66aefc` `vm-migration-feature-branch` build) plus safe, standalone unit-level Go repros run in `/var/folders/.../opencode` with `go 1.26.5`. **No cluster access, no OM API calls, no CR apply, no repo source edits, no live cluster reproduction.** Both attacks are completed as **code inspection** (not runtime reproduction).

### 6.1 A13 — `Process.Port()` nil dereference on missing `net` section

**Source (PR800, verified):** `controllers/om/process.go:210-215`:
```go
func (p Process) Port() string {
	if port, ok := p.Args()["net"].(map[string]interface{})["port"]; ok {
		return cast.ToString(port)
	}
	return ""
}
```

**Panic mechanism (verified):**
- `Process` is `type Process map[string]interface{}` (`controllers/om/process.go:123`).
- `p.Args()` (`:320-322`) returns `util.ReadOrCreateMap(p, "args2_6")` (`pkg/util/util.go:122-127`). `ReadOrCreateMap` creates the `args2_6` sub-map if absent, so `p.Args()` is never nil — but it does **NOT** create the nested `net` sub-map.
- `p.Args()["net"]` — if the `net` key is absent from `args2_6`, the map index returns the `interface{}` zero value (`nil`).
- `nil.(map[string]interface{})` is a **non-comma-ok type assertion** on a nil interface → **panic**. The `ok` in the `if` header is bound by the **map index** `["port"]` on the asserted map, NOT by the type assertion — so it does not guard the assertion. The assertion panics before `ok` is ever evaluated.
- Also panics if `net` is present but not of type `map[string]interface{}` (e.g. a string or nil-valued entry).

**Call sites (PR800, verified):**
- `controllers/om/replicaset.go:389` — `ExtractExternalMembers` (`:381-395`) builds `Hostname: fmt.Sprintf("%s:%s", proc.HostName(), proc.Port())` for every non-disabled process. Reached from the migration plugin at `cmd/kubectl-mongodb/migrate-to-mck/sharded_cluster_generator.go:181` (`return om.ExtractExternalMembers(procs)`).
- `controllers/om/deployment.go:657` — `CheckProcessFields` (`:651-660`) calls `process.Port()`. This site is guarded against a **nil process** (`if process == nil { return false }`, `:653-655`) but NOT against a non-nil process lacking a `net` key. In the operator's own deployment path, processes are constructed by `NewMongodProcess` and always carry `net.port`, so this site is effectively unreachable for operator-built processes; the reachable surface is the plugin consuming an externally-provided automation config.

**Missing test (verified):** `controllers/om/process_test.go:335-353` has `TestPort_ReturnsPortWhenSet` (happy path) and `TestPort_ReturnsEmptyWhenNotSet`. The latter constructs `args2_6: map[string]interface{}{"net": map[string]interface{}{}}` — i.e. the `net` key IS present (as an empty map), so the type assertion succeeds and `["port"]` returns `(nil, false)` → `""`. **No test covers the absent-`net`-key case** (or the wrong-type-`net` case) that triggers the panic.

**Safe repro evidence (unit-level, no cluster/OM/CR):** A standalone Go program in `/var/folders/.../opencode/a13-repro.go` replicates `ReadOrCreateMap`, `Process`, `Args`, and `Port` verbatim, then calls `Port()` on a `Process` with `args2_6: map[string]interface{}{}` (no `net` key). Run with `go 1.26.5`:
```
PANIC (absent net key): interface conversion: interface {} is nil, not map[string]interface {}
---exit:0---
```
Recovered via `defer/recover`; exit 0. This confirms the panic mechanism and the exact runtime message. **This is not a live cluster reproduction** — it exercises the isolated code shape, not the plugin against a real OM automation config.

### 6.2 A14 — `ExtractMemberInfo` panic on missing process in processMap

**Source (PR800, verified):** `controllers/om/replicaset.go:400-406`:
```go
func ExtractMemberInfo(members []ReplicaSetMember, processMap map[string]Process) ([]mdbv1.ExternalMember, string, string) {
	if len(members) == 0 {
		return nil, "", ""
	}
	firstProc := processMap[members[0].Name()]
	version := firstProc.Version()
	fcv := firstProc.FeatureCompatibilityVersion()
	...
```

**Panic mechanism (verified):**
- `processMap[members[0].Name()]` (`:404`) — if the member name is absent from `processMap`, the map index returns the zero value of `Process`, i.e. a **nil map** (since `Process` is `map[string]interface{}`, `:123`).
- `firstProc.Version()` (`:405`) → `controllers/om/process.go:325-327`: `return p["version"].(string)`. On a nil map, `p["version"]` returns `nil`; `nil.(string)` is a non-comma-ok type assertion → **panic** `interface conversion: interface {} is nil, not string`.
- `firstProc.FeatureCompatibilityVersion()` (`:406`) is nil-guarded (`process.go:486-491`: `if p["featureCompatibilityVersion"] == nil { return "" }`) and would be safe on a nil map — but `Version()` is called first (`:405`) and panics first.
- The per-member loop (`:409-419`) has the same unguarded shape: `proc := processMap[host]` (`:411`) returns nil for a missing member; `proc.Args()` would write into a nil map via `ReadOrCreateMap` (`util.go:123-124`, `m[key] = make(...)` on a nil map panics), and `proc.HostName()` (`:416`) → `p["hostname"].(string)` panics on nil. The `:405` panic fires first for `members[0]`.

**Call sites (PR800, verified):** Plugin-only —
- `cmd/kubectl-mongodb/migrate-to-mck/replica_set_generator.go:23` — `externalMembers, version, fcv := om.ExtractMemberInfo(rs.Members(), ac.Deployment.ProcessMap())`.
- `cmd/kubectl-mongodb/migrate-to-mck/sharded_cluster_generator.go:51` — `_, version, fcv := om.ExtractMemberInfo(shardRSes[0].Members(), processMap)`.

Both consume `rs.Members()` and `ac.Deployment.ProcessMap()` read from the OM automation config. No operator-side (reconcile) call site was found for `ExtractMemberInfo`.

**Missing test (verified):** `rg 'ExtractMemberInfo' --type go -g '*_test.go'` across the PR800 tree returns **no matches** — there is no test for `ExtractMemberInfo` at all (neither happy path nor the missing-process panic path).

**Safe repro evidence (unit-level, no cluster/OM/CR):** A standalone Go program in `/var/folders/.../opencode/a14-repro.go` replicates `Process`, `Version`, `FeatureCompatibilityVersion`, and the head of `ExtractMemberInfo`, then calls it with a `processMap` that has no entry for the supplied member name. Run with `go 1.26.5`:
```
PANIC (missing process in map): interface conversion: interface {} is nil, not string
---exit:0---
```
Recovered via `defer/recover`; exit 0. Confirms the panic mechanism and exact runtime message. **Not a live cluster reproduction** — exercises the isolated code shape only.

### 6.3 Verified code defect vs unverified production triggerability

**Verified (code defect):** Both unguarded code paths are confirmed by PR800 source inspection and by safe unit-level repros that reproduce the exact panic messages. The defects are real: a non-comma-ok type assertion on a possibly-nil interface (`Port`), and an unguarded map index into `processMap` followed by a method call on the resulting nil `Process` (`ExtractMemberInfo`).

**Unverified (production triggerability):** Whether either panic is reachable against a **real** Ops Manager automation config is NOT established. Triggerability depends on OM AC schema guarantees that were not verified:
- A13 requires a process whose `args2_6` lacks a `net` key (or has `net` of a non-map type). OM may always populate `net.port` for every `mongod`/`mongos` process; we did not confirm any AC (live or fixture) in which a process lacks `net`.
- A14 requires a replica set member whose name has no matching entry in `ac.Deployment.ProcessMap()`. OM may guarantee that every RS member name always has a corresponding process entry; we did not confirm any AC (live or fixture) where a member name is orphaned from the process map (e.g. a stale member after a partial removal, or a disabled process still referenced by a RS).

The plugin (`kubectl-mongodb migrate-to-mck`) consumes an externally-provided AC; no live AC was observed to trigger either panic, and no live cluster reproduction was performed. The operator-side `Port()` call site (`deployment.go:657`) operates on operator-built processes that always carry `net.port`, so it is not a demonstrated trigger.

**Severity (conservative):** Both classified as **Warning** severity with **Low-Medium triggerability (unverified)**. If triggered, the impact is a plugin/process **crash (panic)** during CR generation or deployment reconciliation — a denial-of-service of the migration tool / reconcile step, recoverable by re-running after correcting the AC. No data loss, no cluster corruption, no persistence of invalid state is implied by the panic itself. Triggerability is downgraded from the defect's confirmed existence because OM schema guarantees are unknown.

**Artifacts written (non-secret, outside repo):** `/var/folders/.../opencode/a13-repro.go`, `/var/folders/.../opencode/a14-repro.go` (standalone repro programs; not committed to the repo). No `bug-hunt/artifacts/` files added for A13/A14 (no cluster manifests produced).

---

## Step 7 — A8: Prune External Members One at a Time (Pair 1 of 3)

**Date:** 2026-07-18.
**Kubeconfig:** `bug-hunt/artifacts/bughunt.kubeconfig` (context `vm-migration-bughunt`).
**Method:** Two live mutations of the MongoDB CR `rs0` (the approved first promote/prune pair), each followed by read-only verification. A third promote/prune pair was planned but **NOT started** — execution stopped after pair 1 per instruction. The external-member removal is **irreversible** (a pruned external member cannot be re-added without re-running the migration add-member path).

**Plan (3 promote/prune pairs):** For each of the 3 external VM members, (1) promote one K8s member to `votes=1, priority=1`, then (2) remove one external member from `spec.externalMembers`. After all 3 pairs, `externalMembers` would be empty and all 3 K8s members would be voting. **Only pair 1 was executed.**

### 7.1 Preflight (read-only)

**Observed (generation 4, phase `Running`):**
- MongoDB CR `rs0`: phase `Running`, `observedGeneration=4`.
- Replica set: **six healthy, in-sync members** — vm-mongodb-0 (PRIMARY), vm-mongodb-1 (SECONDARY), vm-mongodb-2 (SECONDARY), rs0-0 (SECONDARY), rs0-1 (SECONDARY), rs0-2 (SECONDARY). Identical optime across all six.
- Primary: `vm-mongodb-0`.
- PVCs: 3 `gp3` PVCs for rs0-0/rs0-1/rs0-2, all `Bound`.
- Sentinel: `bughunt.sentinel` count = **0** (still absent; consistent with Step 4.8).

### 7.2 Mutation 1 — Promote K8s member rs0-0 (memberConfig[0]) to votes=1, priority=1

**Action:** Patched `spec.memberConfig[0]` (`rs0-0`) from `{votes: 0, priority: "0"}` → `{votes: 1, priority: "1"}`. Single migration change type ("updating member votes/priority") — accepted by the migration change-type validator.

**Observed (generation 5):**
- MongoDB CR `rs0`: `observedGeneration=5`.
- `rs.conf()`: **four voters** (vm-mongodb-0, vm-mongodb-1, vm-mongodb-2, rs0-0). rs0-1/rs0-2 remain non-voting passives (`votes=0`).
- `rs.status()`: all **six members healthy** (health=1), in-sync.

### 7.3 Mutation 2 — Remove externalMembers[2] (rs0_2 / vm-mongodb-2)

**Action:** Removed `rs0_2` (host `vm-mongodb-2`) from `spec.externalMembers` (index 2). Single migration change type ("removing external members") — accepted. **Irreversible:** the pruned external member cannot be re-added without re-running the migration add-member path.

**Observed (generation 6):**
- MongoDB CR `rs0`: `observedGeneration=6`.

### 7.4 Final Read-Only Verification (no further mutations)

**Observed (generation 6, phase `Running`):**
- MongoDB CR `rs0`: phase **`Running`**, `observedGeneration=6`.
- `spec.externalMembers`: **rs0_0, rs0_1** (rs0_2 removed; 2 externals remain).
- `rs.status()`: exactly **five healthy members** (health=1) — vm-mongodb-0, vm-mongodb-1, rs0-0, rs0-1, rs0-2 — with **identical optime** (all in-sync). vm-mongodb-2 is no longer a member.
- Primary: **`vm-mongodb-0`**.
- `rs.conf()`: exactly **3 voters** (vm-mongodb-0, vm-mongodb-1, rs0-0) and **two K8s passives** (rs0-1, rs0-2, `votes=0`).
- PVCs: all rs0 PVCs (rs0-0/rs0-1/rs0-2) `Bound`.
- Pods: all pods (vm-mongodb-0..4, rs0-0..2) `Running`, **0 restarts**.
- Sentinel: `bughunt.sentinel` count = **0** (unchanged; still absent).

### 7.5 OM Public API Verification — Verified (Digest-authenticated read-only GET)

**Action:** Verified OM automation status / goal state via the OM public API (`/api/public/v1.0/groups/$PROJ_ID/automationStatus`) using a Digest-authenticated read-only GET with the existing `my-credentials` Secret credentials. No credential values are recorded here.

**Outcome:** **HTTP 200 — succeeded.** `automationStatus.goalVersion=15`. Exactly **five processes** present; the removed `rs0_2` is **absent**. Every remaining process had `lastGoalVersionAchieved=15`, `errorCode=0`, and an empty `plan` — all agents in goal state at the post-prune membership.

### 7.6 Observation — Stale `NetworkConnectivityVerification` Condition (observedGeneration 3)

**Observed:** The MongoDB CR `rs0` condition `NetworkConnectivityVerification` carries `observedGeneration=3`, while the CR `observedGeneration` is now 6. The condition's `observedGeneration` did not advance through generations 4, 5, 6 despite the spec mutations in 7.2/7.3 and the CR remaining `Running` with `NetworkConnectivityVerification=True`.

**Classification:** **Observation, NOT a confirmed bug.** A stale condition `observedGeneration` may be an expected artifact of the migration reconcile path not re-running connectivity validation once the CR is already `Running` and `NetworkConnectivityVerification=True`, or it may indicate the condition is not being refreshed on migration-only spec changes. The root cause was not investigated. Recorded as an observation for follow-up, not as a confirmed defect.

### 7.7 A8 Result — Partially Complete (Pair 1 of 3)

- **Pair 1: COMPLETE.** Promote rs0-0 (`votes=1, priority=1`) + prune externalMembers[2] (rs0_2/vm-mongodb-2) succeeded. CR `Running` at generation 6; 5 healthy in-sync members; 3 voters (vm-mongodb-0, vm-mongodb-1, rs0-0) + 2 K8s passives; primary vm-mongodb-0; all PVCs Bound; all pods Running 0 restarts; sentinel still 0.
- **Irreversible removal succeeded:** rs0_2/vm-mongodb-2 was removed from `spec.externalMembers` and is no longer a replica set member.
- **OM verification: Verified** (Digest-authenticated read-only GET of `/automationStatus` succeeded; §7.5). `goalVersion=15`; exactly five processes; removed `rs0_2` absent; every remaining process at `lastGoalVersionAchieved=15`, `errorCode=0`, empty `plan` — all agents in goal state.
- **Pairs 2 and 3: NOT started.** Execution stopped before pair 2 per instruction. Remaining work: promote the remaining non-voting K8s members (rs0-1, rs0-2) and prune the remaining external members (rs0_0, rs0_1). After all three pairs, `externalMembers` would be empty.
- **Open observation:** stale `NetworkConnectivityVerification` `observedGeneration=3` (§7.6) — not investigated, not a confirmed bug.

**Artifacts:** None written for A8 (no non-secret manifests produced; mutations were live CR patches, not file artifacts).

---

## Step 8 — A8: Prune External Members One at a Time (Pair 2 of 3)

**Date:** 2026-07-20.
**Kubeconfig:** `bug-hunt/artifacts/bughunt.kubeconfig` (context `vm-migration-bughunt`).
**Method:** Two live mutations of the MongoDB CR `rs0` (the approved second promote/prune pair), each followed by read-only verification. A third promote/prune pair was planned but **NOT started** — execution stopped after pair 2 per instruction. The external-member removal is **irreversible** (a pruned external member cannot be re-added without re-running the migration add-member path).

**Plan (3 promote/prune pairs):** For each of the 3 external VM members, (1) promote one K8s member to `votes=1, priority=1`, then (2) remove one external member from `spec.externalMembers`. After all 3 pairs, `externalMembers` would be empty and all 3 K8s members would be voting. **Pairs 1 and 2 executed; pair 3 NOT started.**

### 8.1 Preflight (read-only, 2026-07-20T12:40Z)

**Observed (generation 6, phase `Running`):**
- MongoDB CR `rs0`: phase `Running`, `observedGeneration=6`, `generation=6`.
- `spec.externalMembers`: **2** (rs0_0/vm-mongodb-0, rs0_1/vm-mongodb-1).
- `spec.memberConfig`: [0]=1/1 (rs0-0 votes=1 priority=1), [1]=0/0 (rs0-1), [2]=0/0 (rs0-2).
- `rs.status()`: **5 members all health=1**, identical optimes (all in-sync). Primary **`vm-mongodb-0`**. `votingMembersCount=3`, `majorityVoteCount=2`, `configVersion=5`, `term=1`.
- `rs.conf()` voters: vm-mongodb-0 (1/1), vm-mongodb-1 (1/1), rs0-0 (1/1). Passives: rs0-1 (0/0), rs0-2 (0/0).
- OM automationStatus: `goalVersion=15`, 5 processes, all `lastGoalVersionAchieved=15`, `errorCode=0`, empty `plan`.
- Sentinel: `bughunt.sentinel` count = **0** (unchanged). PVCs: all rs0 PVCs `Bound`. Pods: all `Running`, 0 restarts.
- **Quorum arithmetic:** current 3 voters / majority 2; after promote 4 voters / majority 3; after prune 3 voters / majority 2. All states maintain live majority.

### 8.2 Mutation 1 — Promote rs0-1 (memberConfig[1]) to votes=1, priority=1

**Action:** Patched `spec.memberConfig[1]` (`rs0-1`) from `{votes: 0, priority: "0"}` → `{votes: 1, priority: "1"}`. Single migration change type ("updating member votes/priority") — accepted by the migration change-type validator.

**Observed (generation 7):**
- MongoDB CR `rs0`: `observedGeneration=7`, phase `Running`.
- OM automationStatus: `goalVersion=16`, 5 processes all at `lastGoalVersionAchieved=16`, `errorCode=0`, empty `plan`.
- `rs.status()`: 5 members all health=1, identical optimes (all in-sync).
- `rs.conf()`: **4 voters** (vm-mongodb-0, vm-mongodb-1, rs0-0, rs0-1), **1 passive** (rs0-2). `configVersion=6`, `term=1`. `votingMembersCount=4`, `majorityVoteCount=3`.
- Pods all `Running`, 0 restarts. PVCs all `Bound`. Sentinel 0.
- **Post-promotion gate: ALL 7 criteria PASS.**

### 8.3 Mutation 2 — Remove externalMembers[1] (rs0_1 / vm-mongodb-1)

**Action:** Removed `rs0_1` (host `vm-mongodb-1`) from `spec.externalMembers` (index 1). Single migration change type ("removing external members") — accepted. **Irreversible:** the pruned external member cannot be re-added without re-running the migration add-member path.

**Observed (generation 8):**
- MongoDB CR `rs0`: `observedGeneration=8`, phase `Running`.
- `spec.externalMembers`: **1** (rs0_0 only).

### 8.4 Final Read-Only Verification (2026-07-20T12:46Z, no further mutations)

**Observed (generation 8, phase `Running`):**
- MongoDB CR `rs0`: phase **`Running`**, `observedGeneration=8`. `spec.externalMembers`: **rs0_0 only** (rs0_1 removed; 1 external remains).
- `rs.status()`: exactly **4 healthy members** (health=1) — vm-mongodb-0, rs0-0, rs0-1, rs0-2 — with **identical optime** (all in-sync). vm-mongodb-1 is no longer a member.
- Primary: **`vm-mongodb-0`** (unchanged since Jul 17, `term=1`).
- `rs.conf()`: exactly **3 voters** (vm-mongodb-0, rs0-0, rs0-1) and **one K8s passive** (rs0-2, `votes=0`). `configVersion=7`, `term=1`.
- OM automationStatus: `goalVersion=17`, exactly **4 processes**; rs0_1 absent. Every remaining process at `lastGoalVersionAchieved=17`, `errorCode=0`, empty `plan`.
- PVCs: all rs0 PVCs `Bound`. Pods: all `Running`, 0 restarts. Sentinel: **0** (unchanged).
- **Post-prune verification: ALL 8 criteria PASS.**

### 8.5 A8 Result — Pair 2 Complete (2 of 3)

- **Pair 2: COMPLETE.** Promote rs0-1 (`votes=1, priority=1`, gen7, 4 voters, 5 healthy) then removed externalMembers[1] rs0_1/vm-mongodb-1 (gen8). Final (gen8, `Running`): 4 healthy in-sync members, primary vm-mongodb-0, `rs.conf` 3 voters (VM0, rs0-0, rs0-1) + 1 K8s passive (rs0-2), `externalMembers=rs0_0` only, all PVCs Bound, all pods Running 0 restarts, sentinel 0.
- **Irreversible removal confirmed:** rs0_1/vm-mongodb-1 removed from `spec.externalMembers` and no longer a replica set member.
- **OM verification:** `goalVersion=17`, 4 processes, rs0_1 absent, all at `lastGoalVersionAchieved=17`/`errorCode=0`/empty `plan`.
- **Pair 3: NOT started.** Stopped per plan. Remaining work: promote rs0-2 and prune rs0_0/vm-mongodb-0 (the primary). Requires a separate reviewed plan.
- **Open observation:** stale `NetworkConnectivityVerification` `observedGeneration=3` (§7.6) — not investigated, not a confirmed bug.

**Artifacts:** None written for A8 pair 2 (no non-secret manifests produced; mutations were live CR patches, not file artifacts).

---

## Step 9 — A10, A13, A14: Source Inspection and Live AC Verification (2026-07-20)

**Date:** 2026-07-20.
**Method:** Static source inspection of the PR800 tree (`/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`, commit `5dc66aefc`) plus live automation config verification via the OM public API (Digest-authenticated read-only GET). No cluster mutations. No credential values recorded.

### 9.1 A10 — Sharded Cert Naming Mismatch (Source Inspection)

**Source (PR800, verified):** The cert Secret naming algorithm was inspected across the generator and operator code paths.

- **Generator side:** The migration plugin sets `certsSecretPrefix` on the generated CR; it does not create per-component Secret names itself.
- **Operator side:** The operator derives per-component cert Secret names from the prefix. For sharded clusters, the expected Secret names are:
  - `{prefix}-{resourceName}-config-cert` (config server)
  - `{prefix}-{resourceName}-mongos-cert` (mongos)
  - `{prefix}-{resourceName}-{i}-cert` (one per shard, where `{i}` is the shard index)
- **User-facing instructions (wrong for sharded):** `mongodb.go:68` and `validation.go:287` tell users to create a single `{prefix}-{resourceName}-cert` Secret. This is the replica-set naming convention. A user following these instructions literally for a sharded cluster would create a Secret that no component looks up.
- **Failure mode:** The operator fails reconciliation with a clear error naming each missing Secret. `certificates.go:425`: `"The secret object '%s' does not contain all the valid certificates needed"`. Not silent — the operator fails safely with an actionable error.
- **Classification:** Documentation/instruction bug, not a code defect. The naming algorithm is consistent (generator sets prefix, operator derives names); the problem is that the user-facing instructions describe replica-set naming, not sharded naming. Severity: Medium.
- **Source:** PR800 tree at `/Users/nam.nguyen/projects/mongodb-kubernetes-pr800`, commit `5dc66aefc`.

### 9.2 A13 — Triggerability Assessment (Source + Live AC)

**Source inspection (PR800, verified):**
- No validation in the plugin checks for `net` key presence in a process's `args2_6`. `validateProcessesAreValid` (`validation.go:422-451`) only checks `processType` and `disabled` fields — it does not inspect `net`.
- The automation config is parsed via `json.Unmarshal` with zero validation or normalization of process fields. `EnsureNetConfig()` exists but is only used on the operator's process-creation path (building processes for K8s deployment), never on the AC-parsing path (consuming an externally-provided OM automation config).
- The unguarded panic path remains as documented in Step 6.1: `Process.Port()` (`controllers/om/process.go:210-215`) performs a non-comma-ok type assertion on `p.Args()["net"]`.

**Live AC verification (2026-07-20, read-only):**
- Queried the live OM automation config via Digest-authenticated read-only GET of `/api/public/v1.0/groups/$PROJ_ID/automationConfig`. No credential values recorded.
- All 4 processes present in the live AC have `args2_6.net.port=27017`. Zero processes lack a `net` key. No violations observed.
- Whether OM's server-side schema guarantees `net` for every process is outside the source tree. The code does not rely on this guarantee.

**Triggerability assessment:** The code is unguarded and will panic if OM ever returns a process without `net`. The live AC shows no violations but cannot prove OM can never produce them. Severity remains Warning / Low-Medium.

### 9.3 A14 — Triggerability Assessment (Source + Live AC)

**Source inspection (PR800, verified):**
- No validation checks that ALL replica set members have corresponding process entries in the process map. `pickSourceProcess` (`validation.go:85-96`) uses an `ok` check but only errors if NO voting+priority member is found — if at least one voting+priority member is present, validation passes even when other members are orphaned from the process map.
- A partial guard exists in `extractInternalClusterAuthMode` (`common_spec.go:158-170`) with a proper `ok`-check on the process map lookup, but it runs AFTER `ExtractMemberInfo` (the panic site at `replicaset.go:400-406`), so it does not protect the panic path.
- The unguarded panic path remains as documented in Step 6.2: `ExtractMemberInfo` (`controllers/om/replicaset.go:400-406`) does `firstProc := processMap[members[0].Name()]` without existence check, then `firstProc.Version()` panics on nil.

**Live AC verification (2026-07-20, read-only):**
- Queried the live OM automation config via Digest-authenticated read-only GET of `/api/public/v1.0/groups/$PROJ_ID/automationConfig`. No credential values recorded.
- All 4 RS member hosts in the live AC match process names exactly. Zero orphaned members. No violations observed.
- Whether OM can produce an orphaned RS member (e.g. a stale member after a partial removal, or a disabled process still referenced by a RS) is outside the source tree. The code does not rely on this guarantee.

**Triggerability assessment:** Same as A13 — unguarded code, live AC clean, OM schema unknown. Severity remains Warning / Low-Medium.

---

## Step 10 — A5: Transition Path Reconstruction (2026-07-20)

**Date:** 2026-07-20.
**Method:** Operator log reconstruction (read-only). No cluster mutations. No credential values recorded. The full chronological path of the MongoDB CR `rs0` from initial creation to `Running` was reconstructed from operator logs, cross-referenced with the read-only observations recorded at Steps 3.3–3.6, 4.8, 4.11, and 5.2.

**Summary:** The CR `rs0` went through at least two incarnations (the CR was deleted and recreated at least once) and two distinct `Failed` phases before reaching `Running`. The final incarnation (UID `5466e872-9a9a-432b-b617-d86c94eac4d9`, recorded at Step 5.2) is the one that reached `Running` and is the current live CR. The total elapsed time from first creation to `Running` was ~58 minutes (14:37:11Z → 16:35:04Z).

**Note:** The explorer reported three UIDs but only one delete/recreate transition is evidenced in the logs. The UID attribution to the first CR (5466e872...) conflicts with the third CR's UID (also 5466e872...), which is impossible since Kubernetes UIDs are unique. The reliable facts are: the CR was deleted and recreated at least once (UID changed to 2f2472ae... at ~14:43:44Z), and the current persistent CR (UID 5466e872..., creationTimestamp 15:03:41Z) is the one that reached Running.

### 10.1 Phase 0 — Operator Startup and CR Creation (14:37:11Z)

- At **2026-07-17T14:37:11Z**, the operator picked up the newly-applied CR `rs0` (first incarnation, UID #1).
- The CR was applied with `mongodb.com/migration-dry-run: "true"` (Step 3.3: `mongodb.mongodb.com/rs0 created`).
- Operator log: `desiredReplicas:0`, `isScaling:false` — the operator correctly handles `members=0` with `externalMembers` present (A1 result confirmed).
- The operator immediately began dry-run connectivity validation.

### 10.2 Phase 1 — First Failed: Network Connectivity Failure (14:37:41Z)

- At **2026-07-17T14:37:41Z**, the dry-run connectivity check failed (~30s after creation).
- Condition: `NetworkConnectivityVerification: False - "Network connectivity failed"`.
- Job `rs0-connectivity-check` failed (Step 3.4).
- Job log evidence: `"Failed to read mongod CA file: open /mongodb-automation/tls/ca/ca-pem: no such file or directory"`.
- Root cause: `rs0-ca` ConfigMap and `mdb-rs0-cert` Secret did not exist yet (created later at Step 3.5).
- CR transitioned to phase `Failed` (Failed #1).

### 10.3 Phase 2 — CR Deleted and Recreated (UID Change, ~14:43:44Z)

- At **~2026-07-17T14:43:44Z**, the CR was deleted and recreated (second incarnation, UID #2).
- Prior to recreation, TLS resources were created (Step 3.5):
  - `configmap/rs0-ca` (keys: `ca-pem`, `mms-ca.crt`) — CA copied from `vm-mongodb-cert` Secret.
  - `secret/mdb-rs0-cert` (keys: `ca.crt`, `tls-combined.pem`, `tls.crt`, `tls.key`) — copied from `vm-mongodb-cert`.
- The recreation triggered a fresh reconcile with TLS resources now in place, re-running the connectivity check.

### 10.4 Phase 3 — External Members Registered, Connectivity Passes (15:03:41Z–15:07:45Z)

- At **2026-07-17T15:03:41Z**, the connectivity check job re-ran and began pinging the 3 external VM members individually.
- At **2026-07-17T15:07:45Z**, the connectivity check passed — `NetworkConnectivityVerification` transitioned to `True` (reason `NetworkValidationPassed`, "All external members reachable and authenticated").
  - This `lastTransitionTime` (`2026-07-17T15:07:45Z`) is corroborated by the Step 4.11 read-only observation.
- All 3 external members pinged individually and confirmed reachable via TLS (CA at `/mongodb-automation/tls/ca/ca-pem`).
- The dry-run connectivity validation phase completed successfully. The CR remained in a non-`Running` state pending agent readiness.

### 10.5 Phase 4 — Second Failed: Agent Readiness Timeout (15:47:14Z–16:32Z)

- At **2026-07-17T15:22:37Z**, the rs0-0 pod was created (pod `startTime` corroborated by Step 4.11).
- At **2026-07-17T15:47:14Z**, the CR transitioned to phase `Failed` for the second time (Failed #2).
- Operator log evidence: `"automation agents haven't reached READY state during defined interval"`.
- The rs0-0 agent was stuck — unable to reach READY state in Ops Manager.
- This `Failed` phase persisted for ~45 minutes (15:47:14Z → 16:32:59Z).
- During this window, the Step 4.8 post-restart snapshot was taken (~16:31Z), observing the CR as `Failed` — see §10.8.

### 10.6 Phase 5 — rs0-0 Agent Recovers, Scaling 0→1→2→3 (16:32:59Z–16:33:55Z)

- At **2026-07-17T16:32:59Z**, the rs0-0 agent finally reached READY state in OM.
- The operator began incremental scaling: `desiredReplicas` 0→1→2→3.
- Each scaling step was triggered by the previous pod's agent reaching READY state in OM.
- rs0-1 pod created (pod `startTime` **2026-07-17T16:33:02Z**, corroborated by Step 4.11).
- At **2026-07-17T16:33:55Z**, rs0-2 was created — rs0-1 and rs0-2 were created in rapid succession.
- rs0-2 pod `startTime` **2026-07-17T16:34:08Z** (corroborated by Step 4.11).
- The scaling from 1→2→3 completed in under 2 minutes, gated only by agent READY-state confirmation in OM.

### 10.7 Phase 6 — Running Achieved (16:35:04Z)

- At **2026-07-17T16:35:04Z**, all 3 K8s agents (rs0-0, rs0-1, rs0-2) reached READY state in OM.
- CR transitioned to phase `Running`, `observedGeneration=4`, `members=3`.
- `memberConfig` initially set to `[{votes:0,priority:0},{votes:0,priority:0},{votes:0,priority:0}]` — all K8s members non-voting. rs0-0 was promoted to `votes:1`/`priority=1` later at generation 5 (A8 pair 1, Step 7.2 on 2026-07-18).
- This is the end-state verified at Step 5.2 (2026-07-18T09:11Z: `resourceVersion=493314`, `generation=4`, `spec.members=3`, `spec.memberConfig` all `votes:0,priority:"0"`, `externalMembers=3`, phase `Running`) and the A7/A8 preflight.
- Consistency note: both independent read-only observations at generation 4 — Step 5.2 (2026-07-18T09:11Z) and the A8 pair 1 preflight (Step 7.1) — confirm all 3 K8s members as `votes:0,priority:"0"`. rs0-0 was promoted to `votes:1`/`priority=1` at generation 5 (A8 pair 1, Step 7.2 on 2026-07-18). No discrepancy.

### 10.8 Confirmation — Step 4.8 "Failed" Snapshot Accuracy

- The Step 4.8 post-restart snapshot (~16:31–16:39Z) recorded the CR `rs0` as phase `Failed` with `NetworkConnectivityVerification=True`.
- **Confirmed accurate:** at ~16:31Z (when the snapshot was taken), the CR was in Failed #2 (agent readiness timeout, 15:47:14Z–16:32:59Z window). The `Failed` phase correctly reflected the live state at that moment.
- The `Failed` phase became stale only after **16:35:04Z** when the CR transitioned to `Running` (Phase 6).
- The Step 4.11 read-only observation (16:39:18Z) correctly shows the CR as `Running` — by that time, Phase 6 had already occurred (16:35:04Z).
- The Step 4.8 RS status query (showing only rs0-0 as a K8s member, rs0-1/rs0-2 absent) is also confirmed: rs0-1 was not created until 16:33:02Z and rs0-2 until 16:34:08Z, both after the Step 4.8 RS status query window.
- Sentinel data was already confirmed absent (count=0) at Step 4.8; not re-queried in this reconstruction.

---

## Step 11 — A8: Prune External Members One at a Time (Pair 3 of 3)

**Date:** 2026-07-20.
**Kubeconfig:** `bug-hunt/artifacts/bughunt.kubeconfig` (context `vm-migration-bughunt`).
**Method:** Two live mutations of the MongoDB CR `rs0` (the approved third and final promote/prune pair), each followed by read-only verification. The external-member removal is **irreversible** (a pruned external member cannot be re-added without re-running the migration add-member path). Pair 3 pruned the current primary (vm-mongodb-0), forcing a MongoDB election.

**Plan (3 promote/prune pairs):** For each of the 3 external VM members, (1) promote one K8s member to `votes=1, priority=1`, then (2) remove one external member from `spec.externalMembers`. After all 3 pairs, `externalMembers` would be empty and all 3 K8s members would be voting. **All 3 pairs executed.**

### 11.1 Preflight (read-only, 2026-07-20T13:18Z)

**Observed (generation 8, phase `Running`):**
- MongoDB CR `rs0`: phase `Running`, `generation=8`, `observedGeneration=8`.
- `spec.externalMembers`: **1** (rs0_0/vm-mongodb-0).
- `spec.memberConfig`: [0]=1/1 (rs0-0 votes=1 priority=1), [1]=1/1 (rs0-1 votes=1 priority=1), [2]=0/0 (rs0-2 votes=0 priority=0).
- `rs.status()`: **4 members all health=1**, identical optimes (all in-sync). Primary **`vm-mongodb-0`**. `votingMembersCount=3`, `majorityVoteCount=2`, `configVersion=7`, `term=1`.
- `rs.conf()` voters: vm-mongodb-0 (1/1), rs0-0 (1/1), rs0-1 (1/1). Passive: rs0-2 (0/0).
- OM automationStatus: `goalVersion=17`, 4 processes, all `lastGoalVersionAchieved=17`, `errorCode=0`, empty `plan`.
- Sentinel: `bughunt.sentinel` count = **0** (unchanged). PVCs: all rs0 PVCs `Bound`. Pods: all `Running`, 0 restarts. Nodes: all `DiskPressure=False` (87% node recovered).
- **Quorum arithmetic:** current 3 voters / majority 2; after promote 4 voters / majority 3; after prune 3 voters / majority 2. All states maintain live majority. Pruning the primary forces an election.

### 11.2 Mutation 1 — Promote rs0-2 (memberConfig[2]) to votes=1, priority=1

**Action:** Patched `spec.memberConfig[2]` (`rs0-2`) from `{votes: 0, priority: "0"}` → `{votes: 1, priority: "1"}`. Single migration change type ("updating member votes/priority") — accepted by the migration change-type validator.

**Observed (generation 9):**
- MongoDB CR `rs0`: `observedGeneration=9`, phase `Running`.
- OM automationStatus: `goalVersion=18`, 4 processes all at `lastGoalVersionAchieved=18`, `errorCode=0`, empty `plan`.
- `rs.status()`: 4 members all health=1, identical optimes (all in-sync).
- `rs.conf()`: **4 voters** (vm-mongodb-0, rs0-0, rs0-1, rs0-2), **0 passives**. `configVersion=8`, `term=1`. `votingMembersCount=4`, `majorityVoteCount=3`.
- Primary still **`vm-mongodb-0`** (no election triggered). Pods all `Running`, 0 restarts. PVCs all `Bound`. Sentinel 0.
- **Post-promotion gate: ALL 7 criteria PASS.**

### 11.3 Mutation 2 — Remove externalMembers[0] (rs0_0 / vm-mongodb-0, the PRIMARY)

**Action:** Removed `rs0_0` (host `vm-mongodb-0`) from `spec.externalMembers` (index 0). Single migration change type ("removing external members") — accepted. **Irreversible:** the pruned external member cannot be re-added without re-running the migration add-member path. This forced a MongoDB election since vm-mongodb-0 was the current primary.

**Observed (generation 10):**
- MongoDB CR `rs0`: `observedGeneration=10`, phase `Running`.
- `spec.externalMembers`: **[]** (empty — migration pruning complete).

### 11.4 Final Read-Only Verification (2026-07-20T13:24Z, no further mutations)

**Observed (generation 10, phase `Running`):**
- MongoDB CR `rs0`: phase **`Running`**, `observedGeneration=10`. `spec.externalMembers`: **[]** (empty — migration pruning complete).
- `rs.status()`: exactly **3 healthy members** (health=1) — rs0-0, rs0-1, rs0-2 — with **identical optime** (all in-sync). vm-mongodb-0 is no longer a member.
- **NEW PRIMARY: `rs0-2`** (`_id=5`) — elected at **2026-07-20T13:22:56Z**, `term=2`. Clean election: rs0-0 voted for rs0-2, all members in-sync, no stale primary, no split brain.
- `rs.conf()`: exactly **3 voters** (rs0-0, rs0-1, rs0-2), **0 passives**, **0 external members**. `configVersion=10`, `term=2`.
- OM automationStatus: `goalVersion=20`, exactly **3 processes**; rs0_0 absent. Every remaining process at `lastGoalVersionAchieved=20`, `errorCode=0`, empty `plan`.
- PVCs: all rs0 PVCs `Bound`. Pods: all `Running`, 0 restarts. Sentinel: **0** (unchanged).
- **Post-prune verification: ALL 9 criteria PASS.**

### 11.5 A8 Result — COMPLETE (All 3 Pairs Executed)

- **Pair 3: COMPLETE.** Promote rs0-2 (`votes=1, priority=1`, gen9, 4 voters, 4 healthy) then removed externalMembers[0] rs0_0/vm-mongodb-0 the PRIMARY (gen10). Election triggered: rs0-2 elected new primary at `term=2`. Final (gen10, `Running`): 3 healthy in-sync members, primary rs0-2, `rs.conf` 3 voters (rs0-0, rs0-1, rs0-2), `externalMembers=[]` (empty), all PVCs Bound, all pods Running 0 restarts, sentinel 0.
- **Irreversible removal confirmed:** rs0_0/vm-mongodb-0 removed from `spec.externalMembers` and no longer a replica set member.
- **OM verification:** `goalVersion=20`, 3 processes, rs0_0 absent, all at `lastGoalVersionAchieved=20`/`errorCode=0`/empty `plan`.
- **A8 ATTACK COMPLETE:** All 3 pairs executed. `externalMembers` is now empty. The replica set is a pure 3-member Kubernetes-native set with all voters, no external members, no passives. Migration pruning is complete.
- **Open observation:** stale `NetworkConnectivityVerification` `observedGeneration=3` (§7.6) — not investigated, not a confirmed bug.

**Artifacts:** None written for A8 pair 3 (no non-secret manifests produced; mutations were live CR patches, not file artifacts).
