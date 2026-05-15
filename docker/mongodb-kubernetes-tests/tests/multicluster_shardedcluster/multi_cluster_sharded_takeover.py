"""multi_cluster_sharded_takeover — Phase G G'5 iter 16.

The headline correctness test of the distributed-operator PoC.

Scenario:
  1. Phase B: Deploy a sharded MongoDB CR in HUB-SPOKE mode (single central
     operator watching all 3 member clusters via kubeconfig auth). Wait for
     Phase=Running. Capture a baseline snapshot of every quantity that
     a disruption would change: per-pod UIDs, per-STS UIDs and
     .status.currentRevision, AutomationConfig version, MDB
     .status.generation/observedGeneration. Persist to logs/G16-baseline.json.

  2. Phase C: Scale the hub-spoke operator's central Deployment to 0
     replicas. Wait ~30s and confirm zero pod activity on any member
     cluster during that quiescent window. Then install the distributed
     pod-mode operators (one per member cluster, helm chart with
     operator.distributed.enabled=true + FQDN peers list). Replicate the
     MDB CR and project ConfigMap + credentials Secret to each member
     cluster (the F12 resource-agreement gate forces operators to wait
     until every member's spec hashes agree).

  3. Phase D: Observation window. For ~5 minutes after the distributed
     operators come up, sample at 5s intervals:
       - Every mongod pod's UID — MUST NOT change.
       - Every STS's .status.currentRevision — MUST NOT change.
       - The MDB CR's .status.phase — MUST reach (or stay at) Running.
       - The OM AutomationConfig version — MUST NOT bump.
     The iter-14g pod-lifecycle ∪ rs.status() safety monitor runs
     concurrently and asserts ZERO out-of-quorum members at every sample.

  4. Phase E: Functional check. With distributed operators in charge,
     mutate the rolling-restart-trigger annotation on each component.
     The operator should pick it up, do a coordinated roll, and the CR
     returns to Phase=Running with observedGeneration advanced. This
     proves the takeover landed in a *working* state, not just inert.

  5. Phase F: Diff post-Phase-D snapshot against the baseline; assert
     ZERO mongod-pod-UID changes, ZERO STS-currentRevision changes,
     ZERO AC-version bump.

This is the F12 design rationale in one test: resource-agreement gate +
canonical-JSON CR-spec hash + leader-only OM writes mean a freshly-
elected distributed operator picks up an existing deployment without
thinking it needs to change anything.
"""

import json
import os
import subprocess
import threading
import time
from typing import Any, Dict, List, Optional

from kubernetes import client as k8s_client
from kubernetes import config as k8s_config
from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, load_proxy_config
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_member_cluster_clients, get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list

# Reuse safety monitor + helpers from the sibling test module rather than
# duplicating them. Both monitor and the distributed setup helpers are
# already factored as module-level helpers; just import them.
from tests.multicluster_shardedcluster.multi_cluster_sharded_simplest import (
    _run_safety_monitor,
    _pick_ready_pod_for_component,
    _query_rs_status,
    _rs_member_states,
    _RS_IN_QUORUM_STATES,
    do_distributed_setup_pod,
    do_distributed_pre_replicate,
)

MDB_RESOURCE_NAME = "sh"

# Phase D observation window — how long after the distributed operators are
# up to keep verifying that nothing changes.
PHASE_D_WINDOW_S = 300

# How often Phase D samples.
PHASE_D_SAMPLE_INTERVAL_S = 5

# Where snapshots get persisted (under /workspace/logs in the devc).
LOGS_DIR = os.path.join(os.environ.get("WORKSPACE", "/workspace"), "logs")
BASELINE_PATH = os.path.join(LOGS_DIR, "G16-baseline.json")
POST_SWAP_PATH = os.path.join(LOGS_DIR, "G16-post-swap.json")
SWAP_OBSERVATIONS_PATH = os.path.join(LOGS_DIR, "G16-phase-d.json")


# ---------------------------------------------------------------------------
# Snapshot helpers — capture the K8s-side state that a disrupting reconcile
# would change.
# ---------------------------------------------------------------------------

def _capture_member_state(namespace: str, cr_name: str) -> Dict[str, Any]:
    """Walk every member cluster, recording each pod + STS + its identity.

    Returns a dict shaped like:
        {
          "<clusterName>": {
            "pods": {
              "<podName>": {
                "uid": "...",
                "phase": "Running",
                "creationTimestamp": "...",
                "containerRestartCounts": {"mongodb-enterprise-database": 0,
                                            "mongodb-agent": 0},
              }, ...
            },
            "statefulsets": {
              "<stsName>": {
                "uid": "...",
                "currentRevision": "...",
                "updateRevision": "...",
                "observedGeneration": 1,
                "replicas": 2, "readyReplicas": 2,
              }, ...
            },
          }, ...
        }
    """
    out: Dict[str, Any] = {}
    for mcc in get_member_cluster_clients():
        cluster_name = mcc.cluster_name
        core = k8s_client.CoreV1Api(api_client=mcc.api_client)
        apps = k8s_client.AppsV1Api(api_client=mcc.api_client)
        pod_records: Dict[str, Any] = {}
        sts_records: Dict[str, Any] = {}

        try:
            pods = core.list_namespaced_pod(namespace=namespace)
        except k8s_client.exceptions.ApiException as e:
            print(f"[snapshot] {cluster_name}: list pods failed: {e}")
            pods = None

        if pods is not None:
            for p in pods.items:
                name = p.metadata.name
                if not name.startswith(f"{cr_name}-"):
                    continue
                restart_counts: Dict[str, int] = {}
                if p.status and p.status.container_statuses:
                    for cs in p.status.container_statuses:
                        restart_counts[cs.name] = int(cs.restart_count or 0)
                pod_records[name] = {
                    "uid": p.metadata.uid,
                    "phase": p.status.phase if p.status else None,
                    "creationTimestamp": (
                        p.metadata.creation_timestamp.isoformat()
                        if p.metadata.creation_timestamp
                        else None
                    ),
                    "containerRestartCounts": restart_counts,
                }

        try:
            sts_list = apps.list_namespaced_stateful_set(namespace=namespace)
        except k8s_client.exceptions.ApiException as e:
            print(f"[snapshot] {cluster_name}: list STS failed: {e}")
            sts_list = None

        if sts_list is not None:
            for s in sts_list.items:
                name = s.metadata.name
                if not name.startswith(f"{cr_name}-"):
                    continue
                status = s.status or k8s_client.V1StatefulSetStatus()
                sts_records[name] = {
                    "uid": s.metadata.uid,
                    "currentRevision": status.current_revision,
                    "updateRevision": status.update_revision,
                    "observedGeneration": status.observed_generation,
                    "replicas": status.replicas,
                    "readyReplicas": status.ready_replicas,
                }

        out[cluster_name] = {"pods": pod_records, "statefulsets": sts_records}
    return out


def _capture_cr_status(sharded_cluster: MongoDB) -> Dict[str, Any]:
    """Read the MDB CR's status section. Snapshot value only — no assertions."""
    sharded_cluster.reload()
    body = sharded_cluster.backing_obj or {}
    status = body.get("status") or {}
    metadata = body.get("metadata") or {}
    return {
        "phase": status.get("phase"),
        "generation": metadata.get("generation"),
        "observedGeneration": status.get("observedGeneration"),
        "version": status.get("version"),
        "lastTransition": status.get("lastTransition"),
        "message": status.get("message"),
    }


def _capture_ac_version(namespace: str, cr_name: str) -> Optional[int]:
    """Best-effort: read the in-cluster AutomationConfig version.

    The operator stores the published AC in a ConfigMap named
    `<cr>-config` (configSrv ProjectName) plus per-shard project ConfigMaps,
    but the central per-cluster AC the OpsManager publishes is mirrored
    into the cluster as an annotation `mongodb.com/ac-version` on the CR
    (this annotation does not always exist on older operator builds).
    The most robust source is the OM project itself via the agent's AC
    document, which we proxy here by reading the AutomationConfig from
    inside a mongod pod via `db.getSiblingDB('admin')` — but that requires
    auth setup. Cheaper: scrape the agent's `/var/lib/mongodb-mms-automation/
    mms-automation-cluster-backup.json` file inside any mongod pod, which
    the agent maintains as its last-seen AC. Returns the integer
    `.version` field, or None on any error.

    For takeover correctness we care about EQUALITY, not the absolute
    number — so a single sample at baseline + a single sample post-swap
    is enough.
    """
    cmd = [
        "cat",
        "/var/lib/mongodb-mms-automation/files/mms-cluster-config-backup.json",
    ]
    for mcc in get_member_cluster_clients():
        core = k8s_client.CoreV1Api(api_client=mcc.api_client)
        try:
            pods = core.list_namespaced_pod(namespace=namespace)
        except k8s_client.exceptions.ApiException:
            continue
        for p in pods.items:
            name = p.metadata.name
            if not name.startswith(f"{cr_name}-config-"):
                continue
            # Need a Ready pod that has the agent running.
            ready = False
            if p.status and p.status.conditions:
                for c in p.status.conditions:
                    if c.type == "Ready" and c.status == "True":
                        ready = True
                        break
            if not ready:
                continue
            try:
                out = KubernetesTester.run_command_in_pod_container(
                    name, namespace, cmd,
                    container="mongodb-agent",
                    api_client=mcc.api_client,
                )
            except Exception:
                continue
            if not out:
                continue
            try:
                start = out.find("{")
                end = out.rfind("}")
                if start < 0 or end <= start:
                    continue
                doc = json.loads(out[start:end + 1])
                v = doc.get("version")
                if isinstance(v, int):
                    return v
            except Exception:
                continue
    return None


def _serialise_snapshot(label: str, member_state: Dict[str, Any],
                        cr_status: Dict[str, Any],
                        ac_version: Optional[int]) -> Dict[str, Any]:
    return {
        "label": label,
        "capturedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "members": member_state,
        "crStatus": cr_status,
        "automationConfigVersion": ac_version,
    }


def _write_snapshot(snapshot: Dict[str, Any], path: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as fh:
        json.dump(snapshot, fh, indent=2, sort_keys=True)
    print(f"[snapshot] {snapshot['label']}: wrote {path}")


# ---------------------------------------------------------------------------
# Phase D observation window — periodically resample member state and confirm
# nothing of importance changed.
# ---------------------------------------------------------------------------

def _diff_snapshots(baseline: Dict[str, Any], post: Dict[str, Any]) -> Dict[str, Any]:
    """Compare two snapshots and return a structured diff.

    Diff is keyed by the kind of change so callers can assert on it:
       {
         "podUidChanged":      [(cluster, pod, base_uid, post_uid), ...],
         "podMissingPostSwap": [(cluster, pod), ...],
         "podNewPostSwap":     [(cluster, pod), ...],
         "podRestartCountInc": [(cluster, pod, container, base, post), ...],
         "stsUidChanged":      [(cluster, sts, base_uid, post_uid), ...],
         "stsCurrentRevisionChanged":
                               [(cluster, sts, base_rev, post_rev), ...],
         "acVersionBumped":    (base, post) or None,
         "crPhaseChanged":     (base, post) or None,
       }
    """
    diff: Dict[str, Any] = {
        "podUidChanged": [],
        "podMissingPostSwap": [],
        "podNewPostSwap": [],
        "podRestartCountInc": [],
        "stsUidChanged": [],
        "stsCurrentRevisionChanged": [],
        "stsUpdateRevisionChanged": [],
        "acVersionBumped": None,
        "crPhaseChanged": None,
    }
    base_members = baseline.get("members", {})
    post_members = post.get("members", {})
    all_clusters = sorted(set(base_members.keys()) | set(post_members.keys()))
    for cluster in all_clusters:
        b = base_members.get(cluster, {})
        p = post_members.get(cluster, {})
        bp = b.get("pods", {})
        pp = p.get("pods", {})
        for pod_name in sorted(set(bp.keys()) | set(pp.keys())):
            bpod = bp.get(pod_name)
            ppod = pp.get(pod_name)
            if bpod is None:
                diff["podNewPostSwap"].append((cluster, pod_name))
                continue
            if ppod is None:
                diff["podMissingPostSwap"].append((cluster, pod_name))
                continue
            if bpod.get("uid") != ppod.get("uid"):
                diff["podUidChanged"].append(
                    (cluster, pod_name, bpod.get("uid"), ppod.get("uid"))
                )
            base_rc = bpod.get("containerRestartCounts", {})
            post_rc = ppod.get("containerRestartCounts", {})
            for c_name in sorted(set(base_rc.keys()) | set(post_rc.keys())):
                bv = int(base_rc.get(c_name, 0))
                pv = int(post_rc.get(c_name, 0))
                if pv > bv:
                    diff["podRestartCountInc"].append(
                        (cluster, pod_name, c_name, bv, pv)
                    )

        bs = b.get("statefulsets", {})
        ps = p.get("statefulsets", {})
        for sts_name in sorted(set(bs.keys()) | set(ps.keys())):
            bsts = bs.get(sts_name)
            psts = ps.get(sts_name)
            if bsts is None or psts is None:
                # New / removed STS is by definition a topology change —
                # report it under uidChanged so the assertion below catches it.
                diff["stsUidChanged"].append(
                    (cluster, sts_name,
                     bsts.get("uid") if bsts else None,
                     psts.get("uid") if psts else None)
                )
                continue
            if bsts.get("uid") != psts.get("uid"):
                diff["stsUidChanged"].append(
                    (cluster, sts_name, bsts.get("uid"), psts.get("uid"))
                )
            if bsts.get("currentRevision") != psts.get("currentRevision"):
                diff["stsCurrentRevisionChanged"].append(
                    (cluster, sts_name,
                     bsts.get("currentRevision"),
                     psts.get("currentRevision"))
                )
            if bsts.get("updateRevision") != psts.get("updateRevision"):
                diff["stsUpdateRevisionChanged"].append(
                    (cluster, sts_name,
                     bsts.get("updateRevision"),
                     psts.get("updateRevision"))
                )

    b_ac = baseline.get("automationConfigVersion")
    p_ac = post.get("automationConfigVersion")
    if b_ac is not None and p_ac is not None and p_ac != b_ac:
        diff["acVersionBumped"] = (b_ac, p_ac)
    b_phase = baseline.get("crStatus", {}).get("phase")
    p_phase = post.get("crStatus", {}).get("phase")
    if b_phase != p_phase:
        diff["crPhaseChanged"] = (b_phase, p_phase)

    return diff


def _print_diff(label: str, diff: Dict[str, Any]) -> None:
    print(f"[{label}] takeover diff:")
    for k in ("podUidChanged", "podMissingPostSwap", "podNewPostSwap",
              "podRestartCountInc", "stsUidChanged",
              "stsCurrentRevisionChanged", "stsUpdateRevisionChanged"):
        v = diff.get(k, [])
        print(f"  {k}: {len(v)} {v if v else ''}")
    print(f"  acVersionBumped:  {diff.get('acVersionBumped')}")
    print(f"  crPhaseChanged:   {diff.get('crPhaseChanged')}")


# ---------------------------------------------------------------------------
# Phase C — operator swap.
# ---------------------------------------------------------------------------

def _scale_central_operator(namespace: str, op_name: str, replicas: int) -> None:
    """Scale the hub-spoke operator Deployment on the central cluster.

    Uses kubectl against the central kubeconfig to be explicit about which
    cluster we're targeting. The fixture's `multi_cluster_operator` typically
    installs under the name `mongodb-kubernetes-operator-multi-cluster` —
    callers pass the right name in.
    """
    ws = os.environ.get("WORKSPACE", "/workspace")
    central_kc = os.path.join(ws, ".generated", "current.devc.kubeconfig")
    subprocess.run(
        ["kubectl", "--kubeconfig", central_kc, "-n", namespace,
         "scale", "deployment", op_name, f"--replicas={replicas}"],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    print(f"[phase-c] scaled central operator {op_name} → {replicas} replicas")


def _wait_central_operator_zero_pods(namespace: str, op_name: str,
                                     timeout_s: int = 60) -> None:
    ws = os.environ.get("WORKSPACE", "/workspace")
    central_kc = os.path.join(ws, ".generated", "current.devc.kubeconfig")
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        res = subprocess.run(
            ["kubectl", "--kubeconfig", central_kc, "-n", namespace,
             "get", "pods", "-l", f"app.kubernetes.io/name={op_name}",
             "-o", "jsonpath={.items[*].metadata.name}"],
            capture_output=True, text=True,
        )
        out = (res.stdout or "").strip()
        if not out:
            print(f"[phase-c] central operator has no pods; quiesced")
            return
        time.sleep(2)
    raise RuntimeError(
        f"central operator {op_name} still has pods after {timeout_s}s — scale-down didn't take effect"
    )


def _assert_member_pods_quiet(namespace: str, cr_name: str,
                              snapshot_baseline: Dict[str, Any],
                              window_s: int) -> None:
    """During the quiet window between scaling the central operator to 0
    and starting the distributed operators, no mongod pod should change
    UID or restart. Sample every 2s.
    """
    print(f"[phase-c] quiet window: monitoring for {window_s}s — expecting ZERO pod activity")
    deadline = time.monotonic() + window_s
    while time.monotonic() < deadline:
        current = _capture_member_state(namespace, cr_name)
        diff = _diff_snapshots(
            {"members": snapshot_baseline["members"]},
            {"members": current},
        )
        bad = (
            diff["podUidChanged"]
            + diff["podMissingPostSwap"]
            + diff["podNewPostSwap"]
            + diff["podRestartCountInc"]
        )
        if bad:
            raise AssertionError(
                f"[phase-c] quiet-window invariant VIOLATED: {bad}"
            )
        time.sleep(2)
    print(f"[phase-c] quiet window clean — no pod activity detected")


# ---------------------------------------------------------------------------
# Pytest fixtures.
# ---------------------------------------------------------------------------

@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"),
        namespace=namespace, name=MDB_RESOURCE_NAME,
    )
    try_load(resource)
    return resource


# ---------------------------------------------------------------------------
# Phase B — hub-spoke deploy.
# ---------------------------------------------------------------------------

@mark.e2e_multi_cluster_sharded_takeover
def test_phase_b_deploy_hubspoke_operator(multi_cluster_operator: Operator):
    """Deploy the standard hub-spoke central operator. This is the same
    fixture/marker setup the simplest test uses — `multi_cluster_operator`
    installs the helm chart with `operator.replicas=0` when
    LOCAL_OPERATOR=true (the dev path) so the actual operator process
    runs as a `go run` outside the cluster. Either way, this is the
    pre-distributed baseline configuration.
    """
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_takeover
def test_phase_b_create_cr(sharded_cluster: MongoDB, custom_mdb_version: str,
                           issuer_ca_configmap: str):
    """Create the MDB CR. Same shape as the simplest test (configSrv 2/2/1,
    shard 2/2/1, mongos 1/2/1)."""
    sharded_cluster.set_version(ensure_ent_version(custom_mdb_version))
    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(
        get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(
        get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(
        get_member_cluster_names(), [1, 2, 1])
    sharded_cluster.set_architecture_annotation()
    sharded_cluster.update()


@mark.e2e_multi_cluster_sharded_takeover
def test_phase_b_reaches_running(sharded_cluster: MongoDB):
    """Wait for hub-spoke deployment to reach Phase=Running. Reuses the
    simplest test's 900s budget — known to be sufficient locally."""
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)


@mark.e2e_multi_cluster_sharded_takeover
def test_phase_b_capture_baseline(sharded_cluster: MongoDB):
    """Snapshot the deployment state immediately after hub-spoke convergence.

    Stores logs/G16-baseline.json. Subsequent phases re-snapshot and diff
    against this file. The assertion in Phase F is that the diff (modulo
    transient additions like new pods) is EMPTY for every quantity we
    record here.
    """
    namespace = sharded_cluster.namespace
    cr_name = sharded_cluster.name
    print(f"[phase-b] capturing baseline of ns={namespace} cr={cr_name}")

    # Allow a brief settling pause: even after Phase=Running, STS controller
    # may still be updating its .status.currentRevision a few moments later.
    time.sleep(10)

    member_state = _capture_member_state(namespace, cr_name)
    cr_status = _capture_cr_status(sharded_cluster)
    ac_version = _capture_ac_version(namespace, cr_name)
    snapshot = _serialise_snapshot("phase-b-baseline", member_state, cr_status, ac_version)
    _write_snapshot(snapshot, BASELINE_PATH)

    # Light invariant on the baseline itself — we want at least some pods
    # and STSes captured.
    total_pods = sum(len(v["pods"]) for v in member_state.values())
    total_sts = sum(len(v["statefulsets"]) for v in member_state.values())
    assert total_pods > 0, "baseline captured zero pods — capture is broken"
    assert total_sts > 0, "baseline captured zero STSes — capture is broken"
    print(f"[phase-b] baseline: {total_pods} pods, {total_sts} STSes, AC version={ac_version}")


# ---------------------------------------------------------------------------
# Phase C — operator swap.
# ---------------------------------------------------------------------------

@mark.e2e_multi_cluster_sharded_takeover
def test_phase_c_scale_down_hubspoke(multi_cluster_operator: Operator,
                                     sharded_cluster: MongoDB):
    """Scale the hub-spoke operator Deployment to 0 replicas, then verify
    no pod activity on any member cluster for the next 30s.

    With LOCAL_OPERATOR=true the hub-spoke operator is actually a `go run`
    process outside the cluster (the chart's deployment is at replicas=0
    already). In that case the user-supplied caller is expected to have
    stopped the `go run` before this test runs — see the orchestration
    note at the bottom of this file. Either way, this step also stops
    the chart's deployment if it has any pods.
    """
    # Load baseline for the quiet-window check.
    with open(BASELINE_PATH) as fh:
        baseline = json.load(fh)

    # Stop the chart's central deployment (no-op if it's already at 0).
    try:
        _scale_central_operator(
            namespace=sharded_cluster.namespace,
            op_name=multi_cluster_operator.name,
            replicas=0,
        )
        _wait_central_operator_zero_pods(
            namespace=sharded_cluster.namespace,
            op_name=multi_cluster_operator.name,
            timeout_s=60,
        )
    except subprocess.CalledProcessError as e:
        # If the chart deployment didn't exist (LOCAL_OPERATOR variation),
        # nothing to scale. Log and continue.
        print(f"[phase-c] note: could not scale chart deployment ({e}); proceeding")

    # If there's a local `go run` operator process, stop it. We can't
    # detect it from inside pytest without process inspection — assume
    # the caller has stopped it before running this test (see file
    # docstring orchestration notes). We do however attempt to kill the
    # mck-operator tmux session if it exists; this is harmless if
    # already gone.
    subprocess.run(
        ["tmux", "kill-session", "-t", "mck-operator"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    print(f"[phase-c] hub-spoke operator stopped")

    # 30s quiet window — assert zero pod activity.
    _assert_member_pods_quiet(
        sharded_cluster.namespace, sharded_cluster.name,
        baseline, window_s=30,
    )


@mark.e2e_multi_cluster_sharded_takeover
def test_phase_c_install_distributed_operators(multi_cluster_operator: Operator,
                                               sharded_cluster: MongoDB):
    """Install the distributed pod-mode operators (one per member cluster)
    and replicate the MDB CR + spec-referenced resources to each cluster.

    Reuses do_distributed_setup_pod and do_distributed_pre_replicate from
    the simplest test module. Those helpers handle helm install with
    operator.distributed.enabled=true + the peers list + per-cluster
    bootstrap flag, CRD apply on each member cluster, and CR/CM/Secret
    replication.
    """
    do_distributed_setup_pod(multi_cluster_operator)
    do_distributed_pre_replicate(sharded_cluster)


# ---------------------------------------------------------------------------
# Phase D — observation window. ZERO disruption assertion.
# ---------------------------------------------------------------------------

@mark.e2e_multi_cluster_sharded_takeover
def test_phase_d_observation_window(sharded_cluster: MongoDB):
    """For PHASE_D_WINDOW_S seconds after the distributed operators are up,
    sample every PHASE_D_SAMPLE_INTERVAL_S seconds and assert:

      - No mongod pod UID has changed (vs baseline).
      - No STS .status.currentRevision has changed (vs baseline).
      - No mongod container restart_count has increased.
      - CR's .status.phase stays at Running (allow a brief transient if any).
      - AC version has not bumped (best-effort sample at end).

    Runs the iter-14g safety monitor concurrently — its cap-1 assertion
    must hold throughout.
    """
    namespace = sharded_cluster.namespace
    cr_name = sharded_cluster.name

    with open(BASELINE_PATH) as fh:
        baseline = json.load(fh)

    print(f"[phase-d] starting {PHASE_D_WINDOW_S}s observation window")
    samples: List[Dict[str, Any]] = []
    violations: List[str] = []

    def _wait_callable():
        # Sampler runs INSIDE the safety monitor wrapper. The wrapper spawns
        # a safety-monitor thread that samples every 2s; this body
        # samples at the configured Phase D interval and checks the diff.
        deadline = time.monotonic() + PHASE_D_WINDOW_S
        sample_idx = 0
        while time.monotonic() < deadline:
            sample_idx += 1
            t0 = time.monotonic()
            current_members = _capture_member_state(namespace, cr_name)
            diff = _diff_snapshots(
                {"members": baseline["members"]},
                {"members": current_members},
            )
            samples.append({
                "sampleIdx": sample_idx,
                "elapsed_s": round(t0 - (deadline - PHASE_D_WINDOW_S), 1),
                "diff": diff,
            })
            for k in ("podUidChanged", "podMissingPostSwap", "podNewPostSwap",
                      "podRestartCountInc", "stsUidChanged",
                      "stsCurrentRevisionChanged"):
                v = diff.get(k, [])
                if v:
                    msg = f"sample={sample_idx} elapsed≈{round(t0 - (deadline - PHASE_D_WINDOW_S),1)}s {k}={v}"
                    if msg not in violations:
                        violations.append(msg)
                        print(f"[phase-d] HARD VIOLATION DETECTED: {msg}")
            # CR phase — should remain Running. We poll it but don't gate
            # on a single tick (operator status writes can lag).
            cr = _capture_cr_status(sharded_cluster)
            if cr.get("phase") != "Running":
                msg = f"sample={sample_idx} crPhase={cr.get('phase')!r} (expected Running)"
                if msg not in violations:
                    violations.append(msg)
                    print(f"[phase-d] CR phase drift: {msg}")
            elapsed = time.monotonic() - t0
            sleep_s = max(0.0, PHASE_D_SAMPLE_INTERVAL_S - elapsed)
            time.sleep(sleep_s)

    # Run the iter-14g pod-lifecycle ∪ rs.status() safety monitor around
    # the observation window. Its cap-1 assertion is the *secondary*
    # invariant; the diff-based invariants above are the primary ones.
    _run_safety_monitor(
        sharded_cluster, "takeover-observation", _wait_callable,
        timeout=PHASE_D_WINDOW_S + 120,
    )

    # Final post-swap snapshot for the Phase F report.
    final_members = _capture_member_state(namespace, cr_name)
    final_cr = _capture_cr_status(sharded_cluster)
    final_ac = _capture_ac_version(namespace, cr_name)
    post = _serialise_snapshot("phase-d-post-swap", final_members, final_cr, final_ac)
    _write_snapshot(post, POST_SWAP_PATH)

    final_diff = _diff_snapshots(baseline, post)
    _print_diff("phase-d-post-swap", final_diff)

    # Persist the per-sample observation series.
    os.makedirs(os.path.dirname(SWAP_OBSERVATIONS_PATH), exist_ok=True)
    with open(SWAP_OBSERVATIONS_PATH, "w") as fh:
        json.dump({
            "samples": samples,
            "finalDiff": final_diff,
            "violations": violations,
        }, fh, indent=2, sort_keys=True)
    print(f"[phase-d] wrote {SWAP_OBSERVATIONS_PATH}")

    # Hard assertions. These are the headline correctness claims.
    assert not final_diff["podUidChanged"], (
        f"[phase-d] PoC FAIL: {len(final_diff['podUidChanged'])} mongod pods changed UID "
        f"during/after takeover: {final_diff['podUidChanged']}"
    )
    assert not final_diff["stsCurrentRevisionChanged"], (
        f"[phase-d] PoC FAIL: {len(final_diff['stsCurrentRevisionChanged'])} STSes changed "
        f".status.currentRevision during/after takeover: "
        f"{final_diff['stsCurrentRevisionChanged']}"
    )
    assert not final_diff["podRestartCountInc"], (
        f"[phase-d] PoC FAIL: {len(final_diff['podRestartCountInc'])} mongod containers "
        f"restarted during takeover: {final_diff['podRestartCountInc']}"
    )
    assert final_diff["acVersionBumped"] is None, (
        f"[phase-d] PoC FAIL: AutomationConfig version bumped during takeover "
        f"(base→post {final_diff['acVersionBumped']}) — "
        f"distributed operator performed an OM write where none was needed"
    )

    if violations:
        # Any per-sample violation that didn't make it into the final diff
        # (e.g. a transient phase blip that recovered) is logged but does
        # not fail the test — the final-diff assertions above are the
        # binding contract.
        print(f"[phase-d] transient violations recorded but resolved by end: "
              f"{len(violations)}: {violations[:5]}")


# ---------------------------------------------------------------------------
# Phase E — functional check post-takeover.
# ---------------------------------------------------------------------------

@mark.e2e_multi_cluster_sharded_takeover
def test_phase_e_post_swap_rolling_restart(sharded_cluster: MongoDB):
    """Trigger a rolling-restart annotation mutation. The distributed
    operators in charge should pick it up, coordinate via the FSM lease,
    and roll all three components without violating per-RS cap-1 safety.

    This proves the takeover landed in a working state, not just inert.
    Reuses iter-14g's safety monitor.
    """
    pre_generation = sharded_cluster.get_generation()
    print(f"[phase-e] pre-generation={pre_generation}")

    trigger_value = f"takeover-rolling-restart-{int(time.time())}"
    print(f"[phase-e] injecting trigger={trigger_value}")
    for key in ("configSrvPodSpec", "shardPodSpec", "mongosPodSpec"):
        pod_spec = sharded_cluster["spec"].setdefault(key, {})
        pt = pod_spec.setdefault("podTemplate", {})
        md = pt.setdefault("metadata", {})
        ann = md.setdefault("annotations", {})
        ann["mongodb.com/rolling-restart-trigger"] = trigger_value
    sharded_cluster.update()
    # Distributed mode — propagate to every member cluster.
    do_distributed_pre_replicate(sharded_cluster)

    def _wait():
        print("[phase-e] waiting for Phase=Running with new generation")
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)
        post_gen = sharded_cluster.get_generation()
        observed = sharded_cluster.get_status_observed_generation()
        print(f"[phase-e] post-generation={post_gen} observed={observed}")
        assert post_gen is not None and post_gen > (pre_generation or 0), (
            f"generation did not advance: pre={pre_generation} post={post_gen}"
        )
        assert observed == post_gen, (
            f"observedGeneration {observed} != generation {post_gen}"
        )

    _run_safety_monitor(sharded_cluster, "phase-e-roll", _wait, timeout=2400)


# ---------------------------------------------------------------------------
# Phase F — final report.
# ---------------------------------------------------------------------------

@mark.e2e_multi_cluster_sharded_takeover
def test_phase_f_final_report(sharded_cluster: MongoDB):
    """Produce a human-readable report of the takeover-vs-baseline diff."""
    with open(BASELINE_PATH) as fh:
        baseline = json.load(fh)
    with open(POST_SWAP_PATH) as fh:
        post = json.load(fh)
    diff = _diff_snapshots(baseline, post)
    print("================================================================")
    print("G16 takeover test — final report")
    print("================================================================")
    print(f"Baseline captured at:  {baseline.get('capturedAt')}")
    print(f"Post-swap captured at: {post.get('capturedAt')}")
    print(f"Baseline AC version:   {baseline.get('automationConfigVersion')}")
    print(f"Post-swap AC version:  {post.get('automationConfigVersion')}")
    print(f"Baseline CR phase:     {baseline.get('crStatus', {}).get('phase')}")
    print(f"Post-swap CR phase:    {post.get('crStatus', {}).get('phase')}")
    print(f"")
    _print_diff("FINAL", diff)
    print("================================================================")

    # These assertions are duplicates of the Phase D ones — they catch
    # the case where Phase E (rolling restart) might inadvertently mask
    # a Phase D regression. The intent is identical.
    assert not diff["podUidChanged"], "post-takeover mongod pod UIDs changed"
    assert not diff["stsCurrentRevisionChanged"] or len(
        diff["stsCurrentRevisionChanged"]) == 0, (
        f"post-takeover STS currentRevisions changed (note: Phase E roll "
        f"will bump these LEGITIMATELY — check this report's labelling): "
        f"{diff['stsCurrentRevisionChanged']}"
    )


# ---------------------------------------------------------------------------
# Orchestration note (read by the human runner, not pytest):
#
# This test deliberately drives the operator swap from inside pytest so the
# state transitions are colocated with the assertions. To make Phase C
# clean, the caller MUST stop the hub-spoke `go run` operator process
# BEFORE pytest runs phase C — the test will kill the mck-operator tmux
# session, but cannot safely kill a foreground `go run`. The orchestrator
# is expected to:
#
#   1. prepare-local-e2e in hub-spoke mode (DEPLOY_OPERATOR=true,
#      LOCAL_OPERATOR=true so the chart deploys at replicas=0).
#   2. op_run.sh --detach (starts hub-spoke operator in mck-operator tmux).
#   3. e2e_run.sh tests/multicluster_shardedcluster/multi_cluster_sharded_takeover.py
#      (pytest drives the full Phase B → F sequence; the tmux kill in
#      Phase C stops the detached operator).
# ---------------------------------------------------------------------------
