# Cloud Native Agent: implementation plan, status and handoff (ST1)

Last updated: 2026-11-02. Scope: ST1 of the "Cloud Native Agent" TD, the split of `mongod`/`mongos` and the agent into separate containers.
ST2 (declarative config), ST3 (stdout logging) and ST4 (coordinated updates) are out of scope here except where noted.


## 0. Where things stand in one paragraph

A replica set (3 members, MongoDB 8.0.4) runs end to end with `mongod` started and stopped by the proxy in its own container, and the agent driving it over localhost HTTP.
This works on a local kind cluster (Kubernetes 1.36.1, arm64) against cloud-qa Ops Manager, with a developer build of the agent, and an e2e test covers it (12 of 12 pass, the test takes about 10 minutes; `destroy` then `all` from nothing was validated with the first 7 tests, in about 3 minutes of test time plus the image builds).
Nothing is pushed or released. The proxy image has no release pipeline yet, the agent change is not in any released agent, and most scenarios beyond "create a replica set, delete a pod" are not tested (section 6).

## 1. Quick start for the next engineer

### 1.1 Repositories, branches, commits (all local, none pushed)

| Repo | Path | Branch | Commits on top of the base |
|---|---|---|---|
| MCK (operator and proxy) | `/Users/igor/work/mongodb-kubernetes` | `KUBE-530/mongodb-proxy` (proxy PR 1732) | `63e8954d2` drop `--magicRestore` from the proxy; `0feecd126` proxy mode in the pod; `04719f1d0` init-database image volume; `2fed32564` no utilities container, hardened e2e test; `087871c15` the local e2e script `scripts/dev/proxy_e2e.sh`; `ec80f2438` proxy logs for exits, stop requests and signals; `3af6be323` e2e tests for restarts initiated by the agent |
| Agent | `/Users/igor/work/mms-automation` | `KUBE-530/mongodb-proxy`, based on `origin/master` at `66964975c4` | `164401ebea` `proxyclient`; `d88c70b8a2` flag `--mongodProxyAddress`; `628d18d3aa` start and stop through the proxy; `775304c1fc` `ProcessRunning` through the proxy; `09251f0b16` no pid of `mongod` in proxy mode (unclean shutdown, Kerberos/LDAP, cpu affinity, CPU metrics); `8f2f35ff20` no `/proc` or `pgrep` lookups (mongos version, unused versions) |

Notes:
- The agent repo also has an old local branch `cloud-native-agent/process-backend` (only the `proxyclient` commit). Ignore it.
- In the agent working tree `Taskfile.yml` (modified) and `tasks/build.yml` (untracked) belong to the previous owner of the checkout. They were never part of these commits. `tasks/build.yml` defines `task build`.

### 1.2 The local environment as it was left

- kind cluster `proxy` (one arm64 node, Kubernetes v1.36.1). Kubeconfig `~/.kube/proxy`, context `kind-proxy`. Use `export KUBECONFIG=$HOME/.kube/proxy`.
- Docker container `kind-registry` (`registry:2`) on `127.0.0.1:5000`, connected to the docker network `kind`.
- Images in that registry (all under `localhost:5000/`, all arm64):

| Image | Tag | Built from |
|---|---|---|
| `mongodb-kubernetes-mongodb-proxy` | `proxydev` | MCK branch (`dev` is stale: it lacks the `rseqDisabled` field) |
| `mongodb-kubernetes` (operator) | `proxydev` | MCK branch at `2fed32564` |
| `mongodb-kubernetes-init-database` | `proxydev` | MCK branch |
| `mongodb-agent` | `109.0.1.9310-1-proxydev2` | released agent image plus the agent branch build (section 1.3, step 2) |

- Namespace `mongodb-test` in that cluster holds: ConfigMap `my-project` (project name `mongodb-test-proxy-8`), Secret `my-credentials` (cloud-qa credentials from `.generated/context.env`), ConfigMap `operator-installation-config`, the CRDs, and the replica set `my-replica-set` left running by the last test run.
- cloud-qa Ops Manager (org taken from `.generated/context.env`) has two projects created by these runs: `mongodb-test-proxy` (automation config for 6.0.5, abandoned) and `mongodb-test-proxy-8` (in use). Delete both when you are done (`scripts/dev/delete_om_projects.sh` exists).
- The scratchpad with the dev agent image build context was lost in a reboot. Section 1.3, step 2 has everything needed to recreate it.

### 1.3 Running the e2e test step by step (no releases needed)

**Shortcut: `scripts/dev/proxy_e2e.sh` in the MCK repo** does steps 1 to 7 below. It is a new file on the MCK branch, not committed yet.

```bash
cd /Users/igor/work/mongodb-kubernetes
scripts/dev/proxy_e2e.sh prereqs     # tools, repositories, context file
scripts/dev/proxy_e2e.sh all         # cluster, images, namespace, clean, test: the whole path from nothing
scripts/dev/proxy_e2e.sh rerun       # clean and test again
scripts/dev/proxy_e2e.sh images agent   # rebuild only one image: proxy, init, operator or agent
scripts/dev/proxy_e2e.sh status      # what exists now
scripts/dev/proxy_e2e.sh cleanup     # delete the namespace; destroy deletes the cluster and the registry
```

Settings are environment variables: `AGENT_REPO` (default `~/work/mms-automation`), `CLUSTER_NAME` (default `proxy`), `MDB_VERSION` (default `8.0.4`), `NEW_PROJECT=1` for a new Ops Manager project (needed when the MongoDB major version changes), `FORCE=1` to recreate an existing cluster. Run it without arguments for the full list.

How it differs from the manual steps below:
- Every build gets a unique image tag (`proxydev-<month day time>`), saved in `~/.cache/proxy-e2e/state`. Image volumes use `IfNotPresent`, so a node keeps an older image that has the same tag. The manual steps use the fixed tag `proxydev` and must rebuild under a new tag after a change of the proxy or init-database image.
- It checks that the cluster exists, so `cluster` is safe to repeat. It connects the registry container to the `kind` network itself, and ignores the final error of `recreate_kind_cluster.sh` after checking that the node is `Ready`.
- It reads the Ops Manager credentials from `.generated/context.env` and never prints them. The project name of the last run is kept in the state file.
- The test log goes to `/tmp/proxy-e2e.log` and the exit code of `pytest` is the exit code of `test`.
- It does not delete the Ops Manager project in cloud-qa: delete it by hand (section 10).

The script passes `shellcheck` with the repo rules. Validation status (2026-11-01): every command was run on the author's machine (macOS, arm64, Docker, kind 1.36.1). `destroy` followed by `all` on a machine without the cluster, the registry and the images created the cluster, built and pushed all images, and ended with 7 passed in about 3 minutes (the test itself, without the builds). `rerun` on an existing stand takes about 6 minutes with the 11 tests. Not validated: other platforms (Linux, amd64) and a machine without the prerequisite tools. Treat the manual steps below as the reference when the script disagrees.

One bug found by that validation is worth knowing: the context file `.generated/context.env` defines `CLUSTER_NAME`, `REGISTRY`, `NAMESPACE` and `KUBECONFIG` with other meanings, so the script never sources it into itself, only into subshells.

Neither the proxy image nor an agent with `--mongodProxyAddress` has been released, so everything the test needs is built locally and served from a local registry. This is what a release would normally provide, and what replaces it:

| Normally from a release | Used instead |
|---|---|
| `mongodb-proxy` image | built from the MCK branch, `localhost:5000/mongodb-kubernetes-mongodb-proxy:proxydev` |
| operator image with the proxy mode | built from the MCK branch, `localhost:5000/mongodb-kubernetes:proxydev` |
| init-database image with the new launcher scripts | built from the MCK branch, `localhost:5000/mongodb-kubernetes-init-database:proxydev` |
| agent image with `--mongodProxyAddress` and the new probe scripts | the released agent image plus a binary built from the agent branch, `localhost:5000/mongodb-agent:109.0.1.9310-1-proxydev2` |
| image tags chosen by the operator from the Ops Manager version | the agent image is forced through the resource (`MDB_PROXY_TEST_AGENT_IMAGE`) |
| `MDB_MONGODB_PROXY_IMAGE` in the operator manifests | passed to the operator by the test fixture through `customEnvVars` |

**Prerequisites** (none of these is installed by the steps below):
- Docker running; `kind`, `kubectl`, `helm` 3.x, Go 1.26.
- The two repositories checked out on branch `KUBE-530/mongodb-proxy`, as `MCK=/Users/igor/work/mongodb-kubernetes` and `AGENT=/Users/igor/work/mms-automation`. The commands below use these two variables.
- A Go module cache that holds the private `github.com/10gen/*` modules (`go env GOMODCACHE` on a machine that can build the agent). The agent build in step 2 borrows it.
- The Python virtualenv of the MCK repo (`$MCK/venv`; `scripts/dev/recreate_python_venv.sh` recreates it).
- The generated context `$MCK/.generated/context.env` with the cloud-qa credentials: it provides `OM_HOST`, `OM_ORGID`, `OM_USER` and `OM_API_KEY`. The context used so far is `e2e_mdb_kind_ubi_cloudqa` (see `scripts/dev/contexts`). Never print this file.
- Network access to `quay.io` (base agent image `quay.io/mongodb/mongodb-agent:109.0.1.9310-1`, server image `quay.io/mongodb/mongodb-enterprise-server:8.0.4-ubi9`), `public.ecr.aws` (Go and busybox images), `github.com` and `cloud-qa.mongodb.com`. No AWS login is needed.
- The node image `kindest/node:v1.36.1` (`docker pull kindest/node:v1.36.1`).
- The commands assume an arm64 machine (Apple Silicon). On amd64 use `TARGETARCH=amd64` in step 2 and a matching node image.

**Step 1. Cluster and registry** (about 3 minutes):

```bash
cd $MCK
KIND_NODE_IMAGE=kindest/node:v1.36.1 scripts/dev/recreate_kind_cluster.sh proxy
docker network connect kind kind-registry
export KUBECONFIG=$HOME/.kube/proxy
kubectl get nodes   # expect one Ready node, v1.36.1
```

- Without `KIND_NODE_IMAGE` the script pulls a node image from an AWS ECR mirror and fails without AWS login.
- The script ends with an error in the CSI driver step (it looks for a context named `proxy-kind`). Ignore it, the test does not need CSI. The kubeconfig is written to `~/.kube/proxy` (context `kind-proxy`), not to `~/.kube/config`.
- The script does not connect the registry container to the `kind` network. Without the second command nodes cannot resolve `kind-registry` and every pull from `localhost:5000` fails. A restart of the container keeps the connection.
- On macOS port 5000 is also taken by AirPlay Receiver. `curl localhost:5000` returns 403 from `AirTunes`. Use `http://127.0.0.1:5000` from the host. Pods are not affected.
- Image volumes work on 1.36.1 without a feature gate.

**Step 2. Build and push the images** (about 10 to 15 minutes the first time). `scripts/release/pipeline.py` and the `make` targets built on it push to ECR and need AWS login, so build directly.

```bash
cd $MCK
make mongodb-proxy-image PROXY_IMAGE=localhost:5000/mongodb-kubernetes-mongodb-proxy:proxydev TARGETARCH=arm64
docker build -f docker/mongodb-kubernetes-init-database/Dockerfile --build-arg version=proxydev \
  -t localhost:5000/mongodb-kubernetes-init-database:proxydev .
docker build -f docker/mongodb-kubernetes-operator/Dockerfile --build-arg version=proxydev --build-arg ldflag_version=proxydev \
  --build-arg log_automation_config_diff=false --build-arg use_race=false -t localhost:5000/mongodb-kubernetes:proxydev .
```

The agent has to be built with cgo (its own `metrics/util` package panics without it) and the cgo build has to run in a Linux container. `task build` from `tasks/build.yml` (tag `mongointernal`) builds without cgo and is not usable for the container. Build in a Go container that borrows the host module cache:

```bash
OUT=$(mktemp -d)   # build context of the dev agent image
docker run --rm -v $AGENT:/src:ro -v "$(go env GOMODCACHE)":/go/pkg/mod:ro -v "$OUT":/out \
  -w /src/go_planner/src/com.tengen/cm -e GOPROXY=off -e GOFLAGS=-mod=mod -e GONOSUMDB='*' \
  public.ecr.aws/docker/library/golang:1.26 \
  sh -c 'CGO_ENABLED=1 go build -buildvcs=false -tags=mongointernal -o /out/automation-agent ./main'
cp $MCK/docker/mongodb-agent/{setup-agent-files.sh,dummy-probe.sh,dummy-readinessprobe} $OUT/
```

The result links against glibc 2.41 and runs on the base agent image (glibc 2.34). Write this `Dockerfile` into `$OUT`:

```dockerfile
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
USER root
COPY automation-agent /agent/mongodb-agent
COPY setup-agent-files.sh /usr/local/bin/setup-agent-files.sh
COPY dummy-probe.sh /usr/local/bin/dummy-probe.sh
COPY dummy-readinessprobe /usr/local/bin/dummy-readinessprobe
RUN chmod +x /agent/mongodb-agent /usr/local/bin/setup-agent-files.sh /usr/local/bin/dummy-probe.sh /usr/local/bin/dummy-readinessprobe
# the cgo-free fallback of os/user needs these for the arbitrary uid of the pod; harmless with a cgo build
ENV USER=mongodb HOME=/tmp
USER 2000
```

```bash
cd $OUT
docker build --build-arg BASE_IMAGE=quay.io/mongodb/mongodb-agent:109.0.1.9310-1 \
  -t localhost:5000/mongodb-agent:109.0.1.9310-1-proxydev2 .
for i in mongodb-kubernetes-mongodb-proxy:proxydev mongodb-kubernetes-init-database:proxydev mongodb-kubernetes:proxydev \
         mongodb-agent:109.0.1.9310-1-proxydev2; do docker push localhost:5000/$i; done
curl -s http://127.0.0.1:5000/v2/_catalog   # expect the four repositories
```

**Step 3. Namespace objects.** Source the context first and set `KUBECONFIG` after it: the context file exports its own `KUBECONFIG`.

```bash
cd $MCK
set -a; source .generated/context.env; set +a
export KUBECONFIG=$HOME/.kube/proxy
kubectl create ns mongodb-test
# A NEW project name for every fresh start: Ops Manager keeps the automation config of a project, and refuses to
# raise featureCompatibilityVersion from 6.0 to 8.0 in one step if an earlier run used another version.
kubectl -n mongodb-test create configmap my-project --from-literal orgId="${OM_ORGID}" \
  --from-literal projectName="mongodb-test-proxy-$(date +%m%d%H%M)" --from-literal baseUrl="${OM_HOST}"
kubectl -n mongodb-test create secret generic my-credentials --from-literal=user="${OM_USER}" --from-literal=publicApiKey="${OM_API_KEY}"
kubectl apply --server-side -f helm_chart/crds
kubectl -n mongodb-test create configmap operator-installation-config \
  --from-literal registry.operator=localhost:5000 --from-literal operator.version=proxydev \
  --from-literal registry.initDatabase=localhost:5000 --from-literal initDatabase.version=proxydev \
  --from-literal registry.agent=localhost:5000 --from-literal agent.version=109.0.1.9310-1-proxydev \
  --from-literal operator.mdbDefaultArchitecture=static \
  --from-literal operator.telemetry.enabled=false --from-literal operator.telemetry.send.enabled=false \
  --from-literal operator.telemetry.collection.clusters.enabled=false \
  --from-literal operator.telemetry.collection.deployments.enabled=false \
  --from-literal operator.telemetry.collection.operators.enabled=false
```

Each project name used leaves a project in cloud-qa. Delete them at the end (section 10).

**Step 4. Clean leftovers of earlier runs.** Skip this on a brand new namespace, do it before every re-run:

```bash
export KUBECONFIG=$HOME/.kube/proxy
kubectl -n mongodb-test delete mdb my-replica-set --ignore-not-found --wait=false
kubectl -n mongodb-test delete pod my-replica-set-0 my-replica-set-1 my-replica-set-2 --grace-period=0 --force --ignore-not-found
kubectl -n mongodb-test delete pvc --all --wait=false    # logs and data live on the PVCs and survive pod restarts
kubectl -n mongodb-test delete deploy mongodb-kubernetes-operator --ignore-not-found   # see the helm conflict below
```

**Step 5. Run the test.** From the MCK repo root (so the chart is `helm_chart` of the repo, not the stale copy in `docker/mongodb-kubernetes-tests/helm_chart`):

```bash
cd $MCK
set -a; source .generated/context.env; set +a
export NAMESPACE=mongodb-test MDB_DEFAULT_ARCHITECTURE=static LOCAL_OPERATOR=true CUSTOM_MDB_VERSION=8.0.4 \
  MDB_MONGODB_PROXY_IMAGE=localhost:5000/mongodb-kubernetes-mongodb-proxy:proxydev \
  MDB_PROXY_TEST_AGENT_IMAGE=localhost:5000/mongodb-agent:109.0.1.9310-1-proxydev2 \
  PYTHONPATH=docker/mongodb-kubernetes-tests KUBECONFIG=$HOME/.kube/proxy
venv/bin/pytest -m e2e_replica_set_mongodb_proxy docker/mongodb-kubernetes-tests/tests/replicaset/replica_set_mongodb_proxy.py \
  --disable-pytest-warnings -s -p no:cacheprovider
```

Why each setting is there:
- `LOCAL_OPERATOR=true` comes from the dev context. The harness then skips the webhook wait (a workstation cannot resolve the in-cluster name `operator-webhook...svc.cluster.local`) but installs the operator with 0 replicas. The fixture `operator_with_proxy` therefore scales the operator deployment to 1 itself. That patch makes Kubernetes record another field owner, so the next `helm upgrade` fails with `conflict occurred while applying object`. Hence the `delete deploy` in step 4.
- `CUSTOM_MDB_VERSION=8.0.4`: the context default 6.0.5 has no build for `rhel 9.8 / aarch64`, so the agent cannot select a version and never calls the proxy.
- `MDB_PROXY_TEST_AGENT_IMAGE` is put into `spec.podSpec.podTemplate` of the resource. The override through `spec.statefulSet` is not applied for replica sets. An overridden agent image also makes the operator skip the agent version it would read from Ops Manager.
- Without `MDB_MONGODB_PROXY_IMAGE` the whole module is skipped.

**Step 6. Expected result.** `7 passed` in about 2.5 minutes. The checks, in order: operator installs; the replica set reaches `Running`; pod spec (no shared process namespace, database container command `/mnt/proxy/proxy`, three image volumes, agent environment); the agent process runs with `-mongodProxyAddress=127.0.0.1:8099` (read from `/proc` in the agent container, because the 5 MB of debug logs make `kubectl logs` unreliable); the proxy reports `running / main` in all pods; `mongod` is visible only in the database container; after the deletion of a pod, the pod is recreated (new uid), the replica set is `Running` again and the new proxy reports `running`.

**Step 7. After a code change, rebuild only what changed**, then repeat steps 4 and 5:

| You changed | Rebuild and push | Notes |
|---|---|---|
| agent code (`mms-automation`) | the cgo agent build and the dev agent image | pods pull the agent image on every start (`pullPolicy: Always`), so delete the pods or the whole resource |
| operator code, `construct/*` | the operator image | `delete deploy` in step 4 makes the next run use it |
| `agent-launcher.sh`, `agent-launcher-lib.sh` | the init-database image | |
| `setup-agent-files.sh`, `dummy-probe.sh`, `dummy-readinessprobe` | the dev agent image | these live in the agent image, not in init-database |
| proxy code | the proxy image under a NEW tag, and update `MDB_MONGODB_PROXY_IMAGE` | image volumes use `IfNotPresent`, the node keeps the old image under the same tag |

### 1.4 When something fails

Start with these (run with `KUBECONFIG=$HOME/.kube/proxy`):

```bash
kubectl -n mongodb-test get pods,mdb
kubectl -n mongodb-test get mdb my-replica-set -o jsonpath='{.status.message}'
# the proxy state, from the agent container (the same way the agent and the test ask it):
kubectl -n mongodb-test exec my-replica-set-0 -c mongodb-agent -- bash -c \
  "exec 3<>/dev/tcp/127.0.0.1/8099; printf 'GET /v1/process HTTP/1.0\r\nHost: p\r\n\r\n' >&3; cat <&3"
# agent errors; use the files, not kubectl logs (the pod log is flooded by debug output):
kubectl -n mongodb-test exec my-replica-set-0 -c mongodb-agent -- \
  grep -a "\[.error\]" /var/log/mongodb-mms-automation/automation-agent.log
kubectl -n mongodb-test logs my-replica-set-0 -c mongodb-enterprise-database   # the proxy and mongod output
kubectl -n mongodb-test logs deploy/mongodb-kubernetes-operator
```

| Symptom | Cause | Fix |
|---|---|---|
| `ImagePullBackOff`, `lookup kind-registry ... no such host` | the registry container is not on the `kind` network | `docker network connect kind kind-registry` |
| `InvalidImageName` for `localhost:5000/mongodb-agent:` (empty tag) | the agent image override did not reach the pod | `MDB_PROXY_TEST_AGENT_IMAGE` is not set, or the override is not in `spec.podSpec.podTemplate` |
| agent panics `Failed to get current user ... requires cgo or $USER` | agent built without cgo and no `USER` and `HOME` in the image | use the cgo build, or the `ENV` lines of the Dockerfile |
| agent panics `must be built with CGO enabled` | agent built without cgo | build as in step 2 |
| proxy returns `400 invalid_request: unknown field "..."` | the proxy image is older than the agent | rebuild the proxy image under a new tag |
| `MongoDB version 6.0.5-ent ... was not found in the list of available versions` | `CUSTOM_MDB_VERSION` is 6.0.5 | `CUSTOM_MDB_VERSION=8.0.4` |
| `featureCompatibilityVersion cannot be upgraded from 6.0 to 8.0` | the Ops Manager project is left from an older run | a new `projectName` in the `my-project` ConfigMap |
| the operator has 0 replicas, or `Operator webhook didn't start` | `LOCAL_OPERATOR` mixed up | `LOCAL_OPERATOR=true` and the fixture scales the operator; do not set it to false on a workstation |
| `helm upgrade ... conflict occurred while applying object` | an earlier run scaled the operator deployment | `delete deploy mongodb-kubernetes-operator` (step 4) |
| plan stuck on `StartFresh`, `a mongo process is already running` | the agent is older than `775304c1fc`: `ProcessRunning` still reads `mongod.lock` and `/proc` | rebuild the agent |
| pod in `Terminating` for 10 minutes | a container ignored SIGTERM (old pods had the utilities container) | `kubectl delete pod ... --grace-period=0 --force`, and use an operator image from `2fed32564` or later |
| `kubectl get crd` shows no `mongodb.com` CRDs, or `no matches for kind` | CRDs not applied | `kubectl apply --server-side -f helm_chart/crds` |
| old logs and data in a fresh pod | the PVC survived | `delete pvc --all` (step 4) |
| `curl localhost:5000` answers 403 | AirPlay Receiver on macOS | use `127.0.0.1:5000` |

## 2. Why

From the TD problem statement: `mongod` runs inside the agent container, so the database resource limits apply to the agent and Ops Manager tracks the agent's metrics instead of the database's.
Goal: dedicated containers with their own resource boundaries.

The agent used to fork, signal and wait for `mongod` itself. Containers of a pod do not share a PID namespace in the new layout, so the agent can no longer do that.
The `mongodb-proxy` takes over exactly those system calls. Everything else stays with the agent: it writes files on the shared `/data` volume and talks to the database through the driver.

## 3. Decisions and the pod design

| Topic | Decision |
|---|---|
| Proxy responsibility | Start, stop and check `mongod`/`mongos`. Nothing else. |
| Process model | PID 1 of the database container, at most one child, the proxy is its direct parent. |
| Fork | The proxy never forks the child. `processManagement.fork: true` in the config is not detected (see risks). |
| Configuration | Passed in the body of the REST request: config file path, role, typed `other` settings. No stdin. |
| `magicRestore` | Not supported by the proxy, not supported in Enterprise Advanced or by the operator. Removed. |
| Oplog streaming | Not done through the proxy. |
| `numactl`, THP | Done by the proxy (`numaInterleave`, `disableTHP`). |
| Database commands | The agent sends them straight to `mongod` with the driver (the shutdown command, readiness checks). |
| Enabling the mode | Agent: `--operatorMode` and `--mongodProxyAddress` both set. Operator: annotation `mongodb.com/v1.mongodbProxy: "true"`, static architecture and `MDB_MONGODB_PROXY_IMAGE` set. If the annotation is set but the rest is not, the operator logs a warning and builds the old pod. |
| Delivery | The proxy is a separate image, mounted as an OCI image volume (Kubernetes 1.36 or newer). |

The pod in proxy mode (static architecture only), compared with the old one:
- Two containers: `mongodb-agent` and `mongodb-enterprise-database` (name unchanged). The old third container `mongodb-agent-operator-utilities` is gone in this mode (see below).
- `ShareProcessNamespace` is not set. In the old static pod it is true.
- The database container runs `/mnt/proxy/proxy`. The proxy image is an image volume `mongodb-proxy` mounted read-only at `/mnt/proxy`. The proxy finds `mongod` through `PATH` (it also has a `-bin-dir` flag).
- The official MongoDB image is an image volume `mongodb-binaries`, mounted read-only into the agent container at `/mnt/mongodb` (`MDB_MONGOD_ROOT`). `agent-launcher.sh` symlinks `mongod`, `mongos` and `mongo` from there, as it used to symlink them from `/proc/<pid>/root`.
- The init-database image is an image volume `mongodb-init-database` mounted read-only into the agent container at `/mnt/init-database` (`MDB_INIT_ROOT`). `setup-agent-files.sh` (scripts) and the two probe scripts of the agent image read it from there, instead of from `/proc/<pid>/root` of the utilities container.
- Agent container environment added: `MDB_MONGODB_PROXY_ADDRESS=127.0.0.1:8099`, `MDB_MONGOD_ROOT`, `MDB_INIT_ROOT`. `agent-launcher.sh` turns the first into `-mongodProxyAddress`.
- `cleanup()` in `agent-launcher-lib.sh` does not signal a pid from `/data/mongod.lock` in this mode (that pid belongs to another container). The proxy gets its own SIGTERM from the kubelet and stops `mongod`.
- Why the utilities container is gone: without a shared namespace it is PID 1 running `tail -F`, which ignores SIGTERM, so every pod deletion waited the whole 600 s grace period. The container only held the init image for `/proc` access, which the image volume replaces.
- The three scripts of the agent image (`setup-agent-files.sh`, `dummy-probe.sh`, `dummy-readinessprobe`) keep the old behaviour when `MDB_INIT_ROOT` is not set, so an agent image built from this branch works with an old operator.

Where in the code (MCK):
- `controllers/operator/construct/mongodb_proxy.go`: constants, `usesMongodbProxy`, the image volume helpers.
- `controllers/operator/construct/database_construction.go`: `buildStaticArchitecturePodTemplateSpec` (containers and volumes) and `buildDatabaseStatefulSetConfigurationFunction` (`ShareProcessNamespace`).
- `controllers/operator/database_statefulset_options.go` and the replica set, standalone, sharded (3 places) and multi-cluster controllers: pass `MDB_MONGODB_PROXY_IMAGE` into the options.
- `pkg/util/constants.go` (`MongodbProxyImageEnv`) and `pkg/images/Imageurls.go`.

## 4. Proxy API (`mongodb-proxy`, default `127.0.0.1:8099`)

- `GET /v1/process`: state `idle`, `running` or `exited`. After an exit: exit code, signal and up to 64 KB of output.
- `POST /v1/process/start`: `configPath`, `role` (`main` or `ephemeral`), `other`, `waitForExit`. Starting again with the same role while the child runs is a no-op.
- `POST /v1/process/stop`: `timeoutSeconds`, `force`, `role`. SIGTERM, then SIGKILL after the timeout when `force` is set (immediately for a zero timeout).
- `other` fields: `binary`, `configExpandRest`, `restore`, `configOnly`, `shardMaintenanceReplSet`, `kerberosKeytab`, `ldapCaPath`, `rseqDisabled`, `disableTHP`, `numaInterleave`.
- Error codes: `invalid_request` 400, `role_conflict` 409, `role_mismatch` 409, `shutting_down` 503, `stop_timeout` 504, `spawn_failed` 500, `canceled` 408.
- Unknown JSON fields are rejected, so a field added on one side must be added on the other. A stale proxy image is therefore a real failure mode: the agent sends `rseqDisabled` for 8.0 and an older proxy answers `400 invalid_request: unknown field "rseqDisabled"`. Rebuild and re-tag the proxy image when its API changes (the node caches an image by tag, the image volume uses `IfNotPresent`).

## 5. Agent side, as implemented

- `cm/proxyclient`: client for the three calls, errors comparable with `errors.Is`, retry only while the connection cannot be established (30 attempts, 1 s apart), no overall HTTP timeout, 14 unit tests.
- Flag `--mongodProxyAddress` (`global/flags/flags.go`, `global.UsingMongodProxy()`): true with `--operatorMode` and an address. An address without `--operatorMode`, or one that is not `host:port`, is a start-up error.
- `cm/mongoctl/proxy.go`: `startMongoViaProxy`, `proxyStartRequest`, `proxyExitError`, `proxyProcessRunning`, `proxyProcessRunningFor`, `forceKillViaProxy`. `processctl.go` branches into it when `global.UsingMongodProxy()`:
  - start: config file written by the new `writeStartConfFile` (never `fork: true` in this mode), switches from the new `startSwitchesFor`. One-shot processes (`ExitsImmediately`, KMIP rotation) use `waitForExit`. A long-running start is watched for 3 s for an early exit, which is mapped to the usual error codes (`UnknownSetParameterErr`, `PortInUseErr`, `OutOfDiskSpaceErr`, `BadValueParameterErr`) that cross-version restore depends on.
  - stop: the driver `shutdown` command is unchanged. "Is it down" and force kill use the proxy.
  - `ProcessRunning(dbpath, configpath)` asks the proxy and compares the config path of the process the proxy runs. This fixed an infinite loop: the pid in `mongod.lock` belongs to another container, so the agent believed `mongod` was down, planned another start and failed with "already running" 866 times.
  - `SleepUntilLocalMongoStartedByDeadline` asks the proxy whether the process is alive.
- Where the pid of `mongod` is no longer used in proxy mode (the pid of `serverStatus` belongs to the proxy's container; `GetPid` itself still works over the wire):
  - `UNCLEAN_SHUTDOWN_PROCESS` (`mmsjob/uncleanshutdown`) kills through `mongoctl.KillViaProxy` instead of `kill -9 <pid>`, which could have hit another process in the agent container.
  - Kerberos keytab and LDAP CA path of the current state (`state/stateutil/processconfigutil.go`, `setKerberosAndLdapCaPath`) are taken from the desired configuration instead of `/proc/<pid>/environ`. A changed path therefore does not restart `mongod` by itself.
  - `numCores`, `cpuAffinity` and `cpuSocketBinding` are cleared with a warning in `config.AssignCoresToProcesses`, the current-state affinity is empty, and the `ChangeCpuAffinity` action does nothing (otherwise the step would be planned for ever).
  - `processcpu.FetchProcessCpuMetrics` (the CPU series of `mongod`/`mongos`) returns nothing. The memory and CPU metrics by pid of `mongot` and `mongotune` are processes of the agent itself and stay.
  - `stateutil.currentMongosVersionString` returns an unknown version (never equal to the desired one) instead of reading the executable in `/proc`; `handleMongosUnableToCommunicateWithConfigServers` then checks the mongos with `Hello`.
  - `Agent.removeUnusedVersionsHelper` returns at once: `pgrep` cannot see `mongod`, so every version would look unused.
- Proxy mode behaviour by design: `Start` returns `NoPid`, the marker file for temporary processes is skipped, `magicRestore` is rejected with an error, `cpuAffinity` (taskset) is ignored with a warning, the `bin` directory is ignored.
- Unit tests: `cm/mongoctl/proxy_unit_test.go` (fake proxy over `httptest`), `*_proxy_test.go` next to each of the places above, `cm/global/flags/operator_flags_unit_test.go`, `cm/proxyclient/client_unit_test.go`.

## 6. Verified and not verified

### 6.1 Verified (kind 1.36.1, arm64, cloud-qa, MongoDB 8.0.4)
- Image volumes for the proxy, the MongoDB image and the init-database image mount and work.
- The agent starts through the init scripts found with `MDB_INIT_ROOT`, symlinks the binaries from `/mnt/mongodb`, and starts `mongod` through the proxy. The replica set reaches `Running`, pods are `Ready` (the readiness and liveness probes work through `MDB_INIT_ROOT`).
- The deletion of a pod: SIGTERM reaches the proxy, the pod is recreated, the proxy is `running` again.
- Restarts initiated by the agent (e2e, 4 tests, 11 of 11 pass): a changed `additionalMongodConfig` (`net.maxIncomingConnections`) restarts `mongod` on all members with the pods and containers untouched, so the agent did it (driver `shutdown`, process-down check, new `start`); `kill -9` on `mongod` (the proxy survives, the agent starts it again); SIGTERM to the proxy, PID 1 (the container restarts, the proxy is idle, the agent starts `mongod`); a restart of the agent container leaves `mongod` alone.
- The same 11 tests pass with the agent that has the no-pid changes (commits `09251f0b16`, `8f2f35ff20`), 2026-11-03: no `environ`, `Error getting pid`, `taskset` or `No such process` lines in the agent log of a pod. The changes were checked by unit tests and by this run only; Kerberos, LDAP, an unclean shutdown job and sharded were not exercised.
- Proxy log evidence (asserted in the test): the old `mongod` exits with code 0 and no signal, the proxy never logs `sending SIGKILL`, a crash is logged as `process exited` with `signal: killed`. The agent never called `stop` on the proxy in these scenarios (no `stop requested` entries): it stops `mongod` with the driver `shutdown` command only, as the TD describes.
- Default pod security context: both containers read and write `/data`.
- `rseqDisabled` is exercised (8.0 on Linux).

### 6.2 Not verified (some of these will fail, none has been tried)
- A `stop` request to the proxy and a force kill. The proxy code exists and has unit tests (`TestLogsForcedStopKills`), but no e2e scenario makes the agent call it (it needs a `mongod` that does not exit on `shutdown`, for example a stop timeout).
- Scale down, version upgrade (in proxy mode a new version changes the `mongodb-binaries` image volume, hence the pod template, so the StatefulSet replaces the pods, not the agent).
- Ephemeral `mongod` (starts with role `ephemeral`, the stop of a leftover one at agent start, `ephemeralPortOffset`, the config path of the ephemeral process) and one-shot starts against a real proxy; unit tests with a fake proxy, and one e2e (the index build below) that starts and exits an ephemeral process through a real proxy. Which flows start an ephemeral `mongod`, and what was found when trying to reach them from an MCK deployment (2026-11-04):
  - The oplog resize does not: it is the command `replSetResizeOplog`.
  - A changed storage field of a standalone (`storage.directoryPerDB`, move `ChangeStorageFieldsStandalone`, a dump and a restore through two ephemeral `mongod`) is tried and **fails before the first ephemeral start**: the agent first backs up the data next to the `dbpath`, `mkdir /data.backup.N` in `/`, and the root file system of the static pod is read-only (`read-only file system`). It is not a proxy problem, the step is mandatory and the same would happen without the proxy. The agent then restarts `mongod` and tries again every few seconds. A standalone in proxy mode itself reaches `Running`.
  - A scale down (move `DisableProcessAndClearReplset` would start one) does not: the operator removes the process from the automation config (`Removed processes as they were removed from replica set`), so the agent has no move for it. Tried with a test, which found no ephemeral start.
  - **Works: the rolling index build** (`rollingindexbuild.go`). For a replica set of 3 or more members each secondary is taken down and the index is built on an ephemeral `mongod`. A test adds `indexConfigs` to the automation config through the Ops Manager API (`OMTester.api_put_automation_config`; the operator does not remove the field) and it passes: all three members get a `start` with role `ephemeral` about 45 s apart, the ephemeral process exits with code 0 after about 3 s, the member starts again with role `main`, and the index exists on every member (`test_agent_runs_an_ephemeral_mongod_to_build_an_index`). The ephemeral process uses the same config path as the main one.
  - Left: the restore jobs (not an MCK path), the stop of a leftover ephemeral process at agent start (unit tests only), a one-shot start (KMIP rotation). `exportsnapshot`: see section 6.3, row 10.
- TLS, authentication, Kerberos, LDAP, `disableTHP` (needs a version before 8.0), `numactl`, `configExpandRest`.
- Sharded and multi-cluster (the option is wired, nothing ran).
- OpenShift, CRI-O, Kubernetes 1.35, other architectures (only arm64), the released agent image.
- AppDB and the community operator (untouched, they use `-skipMongoStart -noDaemonize` and a separate `mongod` container).

### 6.3 Differences from the TD (the TD of 2026-11-01, "TD - Cloud Native Agent_Nov_1.pdf")

What matches the TD: the proxy is PID 1 of the database container and starts `mongod` only on the agent's request; one child, no fork; API `GET /v1/process`, `start`, `stop` with `role`, `waitForExit`, `force`; localhost only; the proxy image is mounted as an OCI image volume (Kubernetes 1.36); the agent runs in proxy mode only with `--operatorMode`; the init-database image is mounted as an image volume instead of the holder container; magic restore and oplog streaming through stdin are not supported; the readiness probe is unchanged.

What differs or is missing. Each item needs a decision or work, and the first four should be agreed with the TD owners.

| # | Topic | TD | Implemented | Action |
|---|---|---|---|---|
| 1 | Activation in MCK | New value `static-ga` of the `mongodb.com/v1.architecture` annotation (ST5, section 5) | Separate annotation `mongodb.com/v1.mongodbProxy: "true"` plus `static` plus `MDB_MONGODB_PROXY_IMAGE` | Move to `static-ga` when ST5 lands |
| 2 | Version gate | `MinAgentVersionForCloudNative`, `MinOMVersionForCloudNative` | None | Implement in MCK (ST5) |
| 3 | Logs of `mongod` | ST3 keeps file logging and the tail, `mongod` writes to a file | The utilities container (which tails the log) is removed in proxy mode, so the `mongod` log no longer reaches `kubectl logs` | Decide: keep a tail in a safe way (a container that exits on SIGTERM) or bring ST3 forward |
| 4 | Agent switch | An interface `ProcessCaller` (status quo) and `ProxyCaller` behind the planner steps | `if global.UsingMongodProxy()` branches inside `mongoctl` | Keep (simpler) or refactor to the interface; agree with the agent team |
| 5 | Mount paths of the init-database image | `/opt/scripts` and `/opt/probes` through `subPath`, no new env var | `/mnt/init-database` and the env var `MDB_INIT_ROOT` read by the scripts | Align with the TD paths, then drop `MDB_INIT_ROOT` |
| 6 | MongoDB binaries in the agent container | Not mentioned: the agent shares only `/data` | The official MongoDB image is mounted at `/mnt/mongodb` in the agent container, the init scripts symlink the binaries from it | Check whether the agent needs the binaries at all (version checks, `mongod` file access); if yes, document it in the TD |
| 7 | Restart of the agent during an ephemeral run | At start the agent calls `GET`, and stops an `ephemeral` child (grace period, then force) | Done: `mongoctl.StopEphemeralViaProxy`, called from `cleanupEphemeralMongoProcesses` at start (2 minutes of grace, then force, role `ephemeral` in the request so that a main process is never signalled); at termination the agent leaves the ephemeral process alone, as the TD says. Unit tests only | Run it against a real proxy |
| 8 | `/proc/<pid>/environ` | The values (Kerberos, LDAP) come from the configuration | Done for the current state (`setKerberosAndLdapCaPath`) | Nothing; the limit is that a changed path does not restart `mongod` |
| 9 | Metrics without `/proc` | Process CPU series stop working, PID-based liveness and kill move to the proxy | Done: liveness, kill, unclean shutdown, cpu affinity, CPU metrics, mongos version, unused versions (section 5) | Nothing for the paths found; `GetPidByConfigPath` and the lock file stay in code that proxy mode does not reach |
| 10 | `exportsnapshot/mongod.go` (CPS export, starts a `mongod` on a snapshot mounted under `DataMount`) | Not in the TD (the TD lists snapshot/D2S3, point in time and selective restore, not CPS export) | Not changed: it goes through the same ephemeral path as the other maintenance starts. It was not checked whether a member runs on the host of such a job, or whether `DataMount` is visible to the database container; without a shared volume the `mongod` cannot start. A guard that refuses the jobs `EXPORT_SNAPSHOT` and `EXPORT_SNAPSHOT_RECOVERY` was tried and reverted: it would be a policy the TD does not state | Ask the TD owners; likely Atlas-only and out of scope for MCK |
| 11 | Image release and version | The proxy version equals the MCK version, same logic as `*-init` (ST7); `proxy.name`, `proxy.version` | Image passed through `customEnvVars` in the test | KUBE-531; this closes the open question on a separate proxy version: no, it follows MCK |
| 12 | Three containers | agent + mongodb + reporter (reporter only headless, ST4) | Two containers | Nothing now; the reporter belongs to ST4 |
| 13 | Community and AppDB | Move to the same model | Not touched | Out of scope for now |
| 14 | E2E coverage (ST6) | Proxy lifecycle: start/stop, ephemerals, one-shot; crash then agent replan | Replica set creation and a pod restart only | See the e2e list in section 7, item 1 |

## 7. Remaining work, in priority order

1. **Agent-initiated stop and restart in e2e.** Done for a config change, a killed `mongod`, a restarted database container and a restarted agent container (section 6.1). Left: a scenario in which the agent has to call `stop` on the proxy (stop timeout, force kill), scale down, and an agent restart during an ephemeral run (needs item 6 of section 6.3). Start the next engineer here.
2. **Remaining PID-based code in the agent.** Done (commits `09251f0b16`, `8f2f35ff20`, section 5): every place that took the pid of `mongod` and used it in `/proc`, `taskset`, `kill` or `pgrep`. Left over: `GetPidByConfigPath`, `getPidFromLockFile` and `util.OnMac()` code are not reached in proxy mode (not removed); metrics of the system and of `mongot` are untouched on purpose; `restore/serverlessrestore.go` is not an MCK path. Not verified: sharded (mongos version fallback), Kerberos and LDAP, an unclean shutdown job from Ops Manager.
3. **Delivery of the proxy image (KUBE-531).** Open decision: repository `quay.io/mongodb/mongodb-kubernetes-mongodb-proxy` and its own version in `release.json`, as for `init-database`? Then: entries in `build_info.json` (patch, staging, release; platforms amd64, arm64, s390x, ppc64le; signing) and `release.json`, Helm values (`registry.mongodbProxy`, `mongodbProxy.name`, `mongodbProxy.version`) and the `MDB_MONGODB_PROXY_IMAGE` variable in `helm_chart/templates/operator.yaml` and `config/manager/manager.yaml`, `RELATED_IMAGE_*` for OLM (digests in disconnected OpenShift), the SSDLC report registration. The e2e test passes the image through `customEnvVars` today. The Dockerfile already builds for all platforms.
4. **A released agent.** Ship an agent with `--mongodProxyAddress` and rebuild the agent image (it contains `setup-agent-files.sh` and the two probes). The agent image and the init-database image must be released together for the new pod to work. Check the interaction with the agent version Ops Manager serves.
5. **Evergreen.** Wire `e2e_replica_set_mongodb_proxy` (task already in `.evergreen-tasks.yml`, no tags, in no variant) into a variant on Kubernetes 1.36 with the proxy image and the new agent. Fix the failing `evergreen/unit_tests` task on PR 1732 (cause unknown; suspects: the Linux THP test and the tests with a fake `mongod`).
6. **Untested scenarios from 6.2**, as separate e2e steps. The largest risks: ephemeral `mongod` during restore (role `ephemeral`, `ephemeralPortOffset`, config path, the one-hour stop timeout), successive upgrade (`BackUpRestoreIsSuccessiveUpgrade`; the agent no longer passes `bin`), TLS and authentication.
7. **Other launch paths:** `exportsnapshot/mongod.go` is decided in section 6.3 (row 10, a question for the TD owners). Not looked at yet: `util.StartMongoCmdWrapper`. The ephemeral path works against a real proxy (the rolling index build e2e, section 6.2); the stop of a leftover ephemeral process at agent start is covered by unit tests only.
8. **Proxy:** done: without the `numactl` binary the start now goes on without it (a warning in the proxy log; before, it failed with `spawn_failed`), and a config file with `processManagement.fork: true` is refused with `400 invalid_request` before anything is spawned (`rejectFork`, read with `sigs.k8s.io/yaml`; an unreadable or unparseable file is left to `mongod`). The proxy tests run on Linux (arm64 container, `-race`). Not done: a `fork: true` that the user puts into `additionalMongodConfig` now makes every start fail with that error; the agent could drop the key in proxy mode instead.
9. **Cleanup and housekeeping:** remove the dead `magicRestore` code in the agent (`RequiresNoForkExecution`, stdin and oplog streaming); add `proxyclient` to `.golangci-pkgs` (the linter was never run locally) and decide on CODEOWNERS; ticket prefixes in the commit messages (`scripts/dev/proxy_e2e.sh` is committed, `087871c15`); push and open the agent PR; log tailing of the database container (border with ST3: the container no longer tails the `mongod` log file).
10. **The differences from the TD** in section 6.3: the decisions on items 1 to 6 first (activation through `static-ga`, version gate, `mongod` logs, `ProxyCaller`, mount paths, binaries in the agent container), then items 7 to 10 in the agent.
11. **ST2 to ST5** of the plan: agent configuration, logging, coordinated updates, the cache of the automation config, the release.

## 8. Risks and open questions

- `processManagement.fork: true` in a config file is refused by the proxy (`invalid_request`). The agent never writes it in proxy mode, but a value from `additionalMongodConfig` would reach the config file and fail every start; the agent should drop it.
- The proxy logs (JSON, `component: proxy`, one line each): `process API listening`, `start requested`, `process started` (with `pid`), `process exited` (`exitCode`, `signal`), `stop requested`, `sending SIGTERM`, `sending SIGKILL`, `forwarding SIGTERM to the child` and `request failed`. `GET` is logged only with the proxy flag `-debug`. Use them to see what the agent asked for; the mongod log lines are in the same stream.
- After a restart of only the database container (for example an OOM kill) the proxy is idle and the database stays down until the agent notices and starts it. Is that acceptable, or is a state file needed?
- Container start order is not defined, hence the connection retries in the client.
- The 3 second early-exit window of a start is a heuristic.
- Errors from the proxy are wrapped into the agent's error type, so `errors.Is(err, proxyclient.ErrRoleConflict)` does not work after the wrap. The message still contains the code.
- The agent has to be built with cgo (its own `metrics/util` package enforces it). In a pod with an arbitrary uid the released agent relies on `nss_wrapper`, which only works for cgo binaries.
- The e2e test is skipped without `MDB_MONGODB_PROXY_IMAGE`, and needs a custom agent until one is released.
- The processes of an earlier run stay in the Ops Manager project, and the operator merges a new process into the existing one (log line `Merged process into existing one`), so options of an earlier run (here `net.maxIncomingConnections: 1234`) can already be in the first automation config of a new resource. A test that sets an option must therefore compare with what `mongod` runs with (`/data/automation-mongod.conf`), not with the resource. It cost two failed runs: the test waited 900 s for a restart that had nothing to restart. Not checked in Ops Manager itself, it is inferred from the agent and operator logs. `proxy_e2e.sh clean` does not clean the project; use `NEW_PROJECT=1` for a fully clean start.
- The Ops Manager project keeps the processes of an earlier run, and a second kind of resource in it is refused: `Cannot have more than 1 MongoDB Cluster per project` (a standalone after a replica set). Use `NEW_PROJECT=1 scripts/dev/proxy_e2e.sh namespace` between different kinds of resources.
- `proxy_e2e.sh clean` first deleted the resource with `--wait=false`; the test loads an existing resource, so now it waits up to 180 s for the deletion.
- The agent tests run `--operatorMode` in `fulltest/test_helpers.go`. This is the reason `--operatorMode` alone does not switch to the proxy.
- `cm/global/test` fails without the `-binary` flag. It is not related to these changes.

## 9. Other documents

This is the only current document. Earlier working notes (in Russian: the original plans for the agent and the proxy, an extract of the TD) were written before several decisions changed, are partly wrong now and are not part of the repository.

## 10. Leftovers to clean up when finished

- kind cluster `proxy` (`scripts/dev/proxy_e2e.sh destroy`), container `kind-registry`, file `~/.kube/proxy`.
- Namespace `mongodb-test` and the replica set in it (`scripts/dev/proxy_e2e.sh cleanup`; `destroy` removes the cluster and the registry too).
- cloud-qa projects `mongodb-test-proxy` and `mongodb-test-proxy-8` (and the one named in `~/.cache/proxy-e2e/state`); the script never deletes Ops Manager projects.
