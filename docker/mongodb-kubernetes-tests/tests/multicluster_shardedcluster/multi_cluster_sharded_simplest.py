"""multi_cluster_sharded_simplest

In default (hub-and-spoke) mode this test deploys a single operator into the
central cluster and a sharded MongoDB CR spanning three member clusters.

Phase D adds an opt-in DISTRIBUTED_POC_MODE branch: when the env var is
"true", the test exercises the distributed-mode design from Phase F/F12:

  * One operator instance per member cluster, coordinated via Raft.
  * Each operator watches ONLY its own member cluster.
  * No operator runs in the central cluster.
  * The MDB CR + spec-referenced ConfigMaps + Secrets are pre-replicated
    to each member cluster identically — the F12 resource-hash agreement
    gate blocks the reconcile until every operator's local observation
    of every ref hashes the same.
  * Phase Running is asserted against the CR's status in any one of the
    member clusters (all three converge to the same status, since the
    leader's StatusReport replicates through the FSM).

Phase G adds a deployment-target switch on top of DISTRIBUTED_POC_MODE:

  DISTRIBUTED_MODE_TARGET=local (default)
    The 3 operators run as `go run` processes in the devcontainer, started
    externally by scripts/dev/run-3-operators-locally.sh and reachable on
    127.0.0.1:7001-7003 (raft) / 8191-8193 (health). CRDs are applied to
    each member cluster via kubectl. No operator pods.

  DISTRIBUTED_MODE_TARGET=pod
    The 3 operators run as Kubernetes Deployments — one per member cluster
    — installed via this repo's helm chart with operator.distributed.enabled
    =true. The chart also creates per-cluster Services exposing the muxed
    raft port; the Istio multi-cluster mesh provides cross-cluster
    reachability via deterministic Service DNS. ServiceAccount auth — no
    kubeconfig mounts.

The rolling-restart test (Phase G G'4) appended at the bottom asserts the
distributed coordinator's safety invariant: at no point during a rolling
restart should more than one voting member be down across the cluster.
That assertion holds in BOTH local and pod modes (it's a coordinator
property, not an infra property).
"""

import json
import os
import subprocess
import time
from typing import List

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

MDB_RESOURCE_NAME = "sh"

# Distributed-mode deployment targets (DISTRIBUTED_MODE_TARGET env). When
# DISTRIBUTED_POC_MODE is on but the target is unset, fall back to LOCAL for
# backwards compatibility with the existing Phase D PoC.
MODE_LOCAL = "local"
MODE_POD = "pod"


def _distributed_poc_mode() -> bool:
    return os.getenv("DISTRIBUTED_POC_MODE", "").lower() in ("true", "1", "yes")


def _distributed_mode_target() -> str:
    """Returns the deployment target for the distributed operators.

    Only meaningful when DISTRIBUTED_POC_MODE is truthy. Accepts "local"
    (default; 3 `go run` processes in the devc) or "pod" (helm-installed
    Deployment per member cluster).
    """
    raw = os.getenv("DISTRIBUTED_MODE_TARGET", "").strip().lower()
    if raw in ("", "local"):
        return MODE_LOCAL
    if raw == "pod":
        return MODE_POD
    raise ValueError(
        f"DISTRIBUTED_MODE_TARGET={raw!r} invalid; expected one of: 'local', 'pod'"
    )


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    try_load(resource)
    return resource


# ---------------------------------------------------------------------------
# Default (hub-and-spoke) path: unchanged.
# ---------------------------------------------------------------------------


@mark.e2e_multi_cluster_sharded_simplest
def test_deploy_operator(multi_cluster_operator: Operator):
    if _distributed_poc_mode():
        target = _distributed_mode_target()
        print(f"[distributed-poc] mode target = {target}")
        if target == MODE_LOCAL:
            do_distributed_setup_local(multi_cluster_operator)
        elif target == MODE_POD:
            do_distributed_setup_pod(multi_cluster_operator)
        else:
            raise AssertionError(f"unreachable mode target: {target}")
        return
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_sharded_simplest
def test_create(sharded_cluster: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
    sharded_cluster.set_version(ensure_ent_version(custom_mdb_version))

    sharded_cluster["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
    sharded_cluster["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
    sharded_cluster.set_architecture_annotation()
    sharded_cluster.update()

    if _distributed_poc_mode():
        # Replicate the CR and all spec-referenced ConfigMaps + Secrets to
        # every member cluster, then re-apply the MDB CR on each member
        # cluster (so the local operators can observe it).
        do_distributed_pre_replicate(sharded_cluster)


@mark.e2e_multi_cluster_sharded_simplest
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=900)


def _voting_component_of(pod_name: str, cr_name: str) -> "str | None":
    """Return the voting-replica-set identifier for a pod, or None if not voting.

    Naming convention (sharded multi-cluster):
      <cr>-config-<clusterIdx>-<podIdx>   → "configSrv"  (voting)
      <cr>-<shardN>-<clusterIdx>-<podIdx> → "shard-<shardN>" (voting)
      <cr>-mongos-<clusterIdx>-<podIdx>   → mongos (NOT voting)

    Shared between rolling-restart, scale-up, and scale-down monitors.
    """
    prefix = cr_name + "-"
    if not pod_name.startswith(prefix):
        return None
    rest = pod_name[len(prefix):]
    if rest.startswith("mongos-"):
        return None
    if rest.startswith("config-"):
        return "configSrv"
    # Shard pod: first token is the shard number.
    shard_token = rest.split("-", 1)[0]
    if shard_token.isdigit():
        return f"shard-{shard_token}"
    return None


def _run_per_rs_notready_monitor(
    sharded_cluster: MongoDB,
    label: str,
    wait_callable,
    timeout: int = 900,
):
    """[INFORMATIONAL ONLY since iter-14g] Per-RS K8s pod-readiness monitor.

    iter-14f conclusively proved that K8s pod-readiness flickers during
    `rs.reconfig()` events on existing pods in non-scaling clusters generate
    false-positive cap violations: the mongod processes don't actually lose
    RS quorum, the agent's readiness endpoint flicks because it's reloading
    AutomationConfig. The actual quorum-safety invariant is now measured by
    `_run_safety_monitor` (which unions pod-lifecycle state and rs.status()
    member state) — not by this K8s-readiness proxy.

    Iter-14g keeps this monitor running alongside the safety monitor purely
    as a diagnostic. It records max NotReady per component and prints a
    summary; callers (and the orchestrator that runs both monitors via
    `_run_safety_monitor`) MUST NOT assert on its output.

    Returns the monitor summary dict for inclusion in per-step reporting.
    """
    import threading

    members = list(get_member_cluster_names())
    member_clients = list(get_member_cluster_clients())
    stop_event = threading.Event()
    seen_components: List[str] = []
    max_notready_per_component: dict = {}  # component -> max NotReady observed
    sample_count: List[int] = [0]
    lock = threading.Lock()

    def monitor():
        namespace = sharded_cluster.namespace
        cr_name = sharded_cluster.name
        while not stop_event.is_set():
            try:
                notready: dict = {}
                for mcc in member_clients:
                    api = k8s_client.CoreV1Api(api_client=mcc.api_client)
                    pods = api.list_namespaced_pod(namespace=namespace, label_selector="")
                    for p in pods.items:
                        name = p.metadata.name
                        comp = _voting_component_of(name, cr_name)
                        if comp is None:
                            continue
                        ready = False
                        if p.status and p.status.conditions:
                            for c in p.status.conditions:
                                if c.type == "Ready" and c.status == "True":
                                    ready = True
                                    break
                        if not ready:
                            notready[comp] = notready.get(comp, 0) + 1
                with lock:
                    sample_count[0] += 1
                    for comp, n in notready.items():
                        prev = max_notready_per_component.get(comp, 0)
                        if n > prev:
                            max_notready_per_component[comp] = n
                        if comp not in seen_components:
                            seen_components.append(comp)
            except Exception as e:
                print(f"[{label}/k8s-monitor] poll error: {e}")
            stop_event.wait(2.0)

    monitor_thread = threading.Thread(target=monitor, daemon=True, name=f"{label}-k8s-monitor")
    monitor_thread.start()

    try:
        wait_callable()
    finally:
        stop_event.set()
        monitor_thread.join(timeout=10)

    print(
        f"[{label}] k8s-readiness monitor (INFORMATIONAL): samples={sample_count[0]} "
        f"components_seen={seen_components} "
        f"max_notready_per_component={max_notready_per_component}"
    )

    return {
        "samples": sample_count[0],
        "components_seen": seen_components,
        "max_notready_per_component": dict(max_notready_per_component),
    }


# ---------------------------------------------------------------------------
# Combined per-RS safety monitor (iter-14g):
#
# A voting mongod is "out of quorum" iff EITHER of the following two signals
# says so. Both are sampled concurrently every ~2-3s; the union is the
# assertion:
#
#   1. POD-LIFECYCLE   (K8s side, directly observes the mongod process state)
#       - pod.status.phase                 must be "Running"
#       - pod.metadata.deletion_timestamp  must be None (not Terminating)
#       - container `mongodb-enterprise-database`:
#           state.running                  must be non-None
#           restart_count                  must NOT have increased since
#                                          the previous sample (catches
#                                          crash/kill/restart events that
#                                          happen entirely between samples)
#       This deliberately does NOT include the Ready condition: readiness
#       probe flickers during AutomationAgent AC reloads (iter-14f finding)
#       generate false positives that the mongod process itself does not
#       experience.
#
#   2. REPLSET-STATE   (MongoDB side, directly observes the RS view)
#       - For each voting RS, every sample pick a Ready mongod pod and
#         kubectl-exec mongosh to parse `rs.status()`.
#       - A member with state != PRIMARY (1) AND state != SECONDARY (2)
#         counts as out-of-quorum.
#       - health != 1 also counts as out-of-quorum.
#       - If rs.status() is unavailable this sample (transient mongosh
#         failure during reconfig), the pod-lifecycle signal is used alone
#         for that tick.
#
# Cap-1 invariant: at most one voting member per RS observed as
# out-of-quorum at any sample, summed across both signals.
#
# Plus a post-step QUIESCE check: at test-step end, every RS member must
# be in state PRIMARY or SECONDARY. That asserts we returned to a healthy
# steady state, not that "we stayed healthy the whole time".
#
# The k8s-readiness monitor stays as INFORMATIONAL diagnostic — its output
# is printed but no longer asserted.
# ---------------------------------------------------------------------------

# replSetGetStatus member.state values that count as in-quorum for the
# cap-1 invariant. ARBITER (7) is excluded by design: the simplest fixture
# has no arbiters, and any voting member transitioning into ARBITER state
# mid-test would be an unexpected reconfig event we want to flag.
_RS_IN_QUORUM_STATES = {1, 2}  # PRIMARY, SECONDARY


def _pod_member_address(pod_name: str, namespace: str) -> str:
    """Return the rs.status() member name for a given mongod pod.

    The operator wires every mongod to a per-pod headless service with the
    same name as the pod plus a `-svc` suffix. `rs.status()` reports each
    member as `<pod>-svc.<namespace>.svc.cluster.local:27017`.
    """
    return f"{pod_name}-svc.{namespace}.svc.cluster.local:27017"


def _coerce_int(v) -> int:
    """mongosh's `JSON.stringify` serialises NumberInt as plain ints, but
    a few fields (notably timestamps and some legacy NumberLong wraps) may
    appear as {"$numberInt": "1"} or {"high": 0, "low": 1}. Accept either.
    """
    if isinstance(v, dict):
        if "$numberInt" in v:
            try:
                return int(v["$numberInt"])
            except Exception:
                return 0
        if "low" in v:
            try:
                return int(v["low"])
            except Exception:
                return 0
        return 0
    try:
        return int(v)
    except Exception:
        return 0


def _pod_lifecycle_serving(
    pod,
    prev_restart_counts: dict,
) -> "tuple[bool, str]":
    """Return (serving, reason) for a single voting pod.

    serving=True iff:
      - pod.status.phase == "Running"
      - pod.metadata.deletionTimestamp is None
      - container `mongodb-enterprise-database` exists, has state.running
        non-None, and restart_count has NOT increased since prev_restart_counts.

    Updates prev_restart_counts with the latest restart_count regardless of
    outcome (so a single restart event flags exactly one sample, not the
    rest of the run).
    """
    name = pod.metadata.name
    phase = pod.status.phase if pod.status else None
    if phase != "Running":
        return False, f"phase={phase}"
    if pod.metadata.deletion_timestamp is not None:
        return False, "terminating"
    if not pod.status or not pod.status.container_statuses:
        return False, "no-container-statuses"
    mongod_cs = None
    for cs in pod.status.container_statuses:
        if cs.name == "mongodb-enterprise-database":
            mongod_cs = cs
            break
    if mongod_cs is None:
        return False, "no-mongod-container"
    if mongod_cs.state is None or mongod_cs.state.running is None:
        return False, "not-running"
    rc = int(mongod_cs.restart_count or 0)
    prev_rc = prev_restart_counts.get(name)
    prev_restart_counts[name] = rc
    if prev_rc is not None and rc > prev_rc:
        return False, f"restart_count {prev_rc}->{rc}"
    return True, "ok"


def _pick_ready_pod_for_component(
    sharded_cluster: MongoDB,
    component: str,
) -> "tuple[str, str, object] | None":
    """Return (pod_name, namespace, api_client) for some Ready mongod pod
    belonging to the given component, or None if nothing usable exists
    yet. Used as a kubectl-exec target for the rs.status() sampler.

    "Ready" here means the K8s Ready condition is True — slightly stricter
    than the lifecycle predicate (because we need mongosh to actually
    connect via localhost:27017 inside the pod), and acceptable because
    losing the Ready condition transiently just means we skip this sample
    for that component and rely on the pod-lifecycle signal.
    """
    namespace = sharded_cluster.namespace
    cr_name = sharded_cluster.name
    for mcc in get_member_cluster_clients():
        try:
            api = k8s_client.CoreV1Api(api_client=mcc.api_client)
            pods = api.list_namespaced_pod(namespace=namespace, label_selector="")
            for p in pods.items:
                if _voting_component_of(p.metadata.name, cr_name) != component:
                    continue
                ready = False
                if p.status and p.status.conditions:
                    for c in p.status.conditions:
                        if c.type == "Ready" and c.status == "True":
                            ready = True
                            break
                if ready:
                    return (p.metadata.name, namespace, mcc.api_client)
        except Exception:
            continue
    return None


def _query_rs_status(pod_name: str, namespace: str, api_client) -> "dict | None":
    """kubectl-exec into a mongod pod and run mongosh to retrieve
    `rs.status()` as JSON. Returns the parsed dict or None on any error.
    """
    cmd = [
        "/var/lib/mongodb-mms-automation/bin/mongosh",
        "--quiet",
        "--host",
        "localhost",
        "--port",
        "27017",
        "--eval",
        "JSON.stringify(rs.status())",
    ]
    try:
        out = KubernetesTester.run_command_in_pod_container(
            pod_name,
            namespace,
            cmd,
            container="mongodb-enterprise-database",
            api_client=api_client,
        )
    except Exception:
        return None
    if not out:
        return None
    start = out.find("{")
    end = out.rfind("}")
    if start < 0 or end <= start:
        return None
    try:
        return json.loads(out[start : end + 1])
    except Exception:
        return None


def _rs_member_states(rs_status: dict) -> "dict[str, tuple[int, int]]":
    """Return a {member_name: (state, health)} map from a parsed rs.status()
    document. Empty map if the doc has no members array.
    """
    out: dict = {}
    if not isinstance(rs_status, dict):
        return out
    for m in rs_status.get("members", []) or []:
        try:
            name = m.get("name")
            if not name:
                continue
            state = _coerce_int(m.get("state", 0))
            health = _coerce_int(m.get("health", 0))
            out[name] = (state, health)
        except AttributeError:
            continue
    return out


def _run_safety_monitor(
    sharded_cluster: MongoDB,
    label: str,
    wait_callable,
    timeout: int = 1500,
    components: "list[str] | None" = None,
    sample_interval_s: float = 2.0,
):
    """Run the combined per-RS safety monitor around `wait_callable`.

    For each component in `components` (default: configSrv, shard-0),
    every `sample_interval_s` seconds:

      1. List voting pods across all member clusters; compute
         pod_lifecycle_serving for each, updating restart_count memory.
      2. Pick one Ready pod for the component; kubectl-exec mongosh to
         retrieve rs.status(); build {member -> (state, health)}.
      3. A pod is "out of quorum" iff its lifecycle predicate is False OR
         its rs-state is not in {PRIMARY, SECONDARY}.
      4. Per-component count those out-of-quorum pods; assert ≤ 1.

    On `wait_callable` return, run a QUIESCE check: every member of every
    component's rs.status() must be in PRIMARY or SECONDARY state.

    The legacy K8s-readiness monitor (`_run_per_rs_notready_monitor`) is
    started concurrently as a diagnostic — its summary is printed but
    nothing is asserted on it.
    """
    import threading

    if components is None:
        components = ["configSrv", "shard-0"]

    namespace = sharded_cluster.namespace
    cr_name = sharded_cluster.name
    stop_event = threading.Event()
    lock = threading.Lock()

    failures: List[str] = []
    sample_count: List[int] = [0]
    rs_query_failures_per_component: dict = {comp: 0 for comp in components}
    max_out_per_component: dict = {}
    seen_components: List[str] = []

    # restart_count memory per pod — shared across samples so a +1 only
    # fires once.
    prev_restart_counts: dict = {}

    # Per-sample reasoning buffer for the first violation so we can surface
    # WHICH signal flagged a pod (lifecycle vs rs-state).
    flagged_reasons_per_component: dict = {comp: [] for comp in components}

    def sample_once():
        # Step 1: enumerate voting pods per component across clusters.
        pods_by_component: dict = {comp: [] for comp in components}
        cluster_clients = list(get_member_cluster_clients())
        for mcc in cluster_clients:
            try:
                api = k8s_client.CoreV1Api(api_client=mcc.api_client)
                pods = api.list_namespaced_pod(namespace=namespace, label_selector="")
                for p in pods.items:
                    comp = _voting_component_of(p.metadata.name, cr_name)
                    if comp in pods_by_component:
                        pods_by_component[comp].append(p)
            except Exception as e:
                # Cluster-level transient — skip this cluster for this
                # sample but keep the others.
                continue

        # Step 2: for each component, query rs.status() once.
        rs_states_per_component: dict = {}
        rs_query_ok: dict = {}
        for comp in components:
            target = _pick_ready_pod_for_component(sharded_cluster, comp)
            if target is None:
                rs_query_ok[comp] = False
                continue
            pod_name, ns, api_client = target
            rs_status = _query_rs_status(pod_name, ns, api_client)
            if rs_status is None:
                rs_query_ok[comp] = False
                continue
            rs_query_ok[comp] = True
            rs_states_per_component[comp] = _rs_member_states(rs_status)

        # Step 3: per-component, count out-of-quorum pods.
        #
        # Counting rule: a pod is counted as out-of-quorum ONLY if it IS
        # currently part of the RS (in rs.status() members) AND either
        # pod-lifecycle says not-serving OR rs-state is not in {PRIMARY,
        # SECONDARY} OR health != 1.
        #
        # Pods that exist in K8s but are MISSING from the RS member set
        # (mid-add — the STS has created the pod but `rs.reconfig()`
        # hasn't yet added it as a voting member; or mid-remove — the
        # reverse) are NOT counted. They are not voting members of the
        # RS, so by definition they cannot violate a per-RS quorum
        # invariant. The cap-1 claim is about EXISTING voting members
        # of the RS, not about pending additions.
        #
        # If the rs.status() query failed for this sample, fall back to
        # the pod-lifecycle signal alone — that still catches a real
        # mongod crash on an existing member. The per-component
        # rs_query_failures counter tracks how often that happens so we
        # know whether the sampler is healthy.
        per_component_out: dict = {}
        per_component_reasons: dict = {}
        for comp, pods in pods_by_component.items():
            if not pods:
                continue
            states = rs_states_per_component.get(comp, {})
            rs_query_succeeded = bool(rs_query_ok.get(comp, False))
            out_count = 0
            reasons: list = []
            for p in pods:
                pod_name = p.metadata.name
                serving, lifecycle_reason = _pod_lifecycle_serving(
                    p, prev_restart_counts
                )
                rs_reason = "rs:n/a"
                count_this_pod = True
                if rs_query_succeeded:
                    addr = _pod_member_address(pod_name, namespace)
                    st = states.get(addr)
                    if st is None:
                        # Pod is K8s-present but the RS doesn't list it
                        # as a member (yet, or anymore). It is NOT a
                        # voting member of the RS right now, so it
                        # cannot violate the per-RS quorum invariant.
                        # Skip without counting; record for visibility.
                        rs_reason = "rs:not-yet-member"
                        count_this_pod = False
                    else:
                        state, health = st
                        rs_reason = f"rs:state={state}/health={health}"
                        if state not in _RS_IN_QUORUM_STATES or health != 1:
                            serving = False
                if count_this_pod and not serving:
                    out_count += 1
                    reasons.append(
                        f"{pod_name}: lifecycle={lifecycle_reason} {rs_reason}"
                    )
            per_component_out[comp] = out_count
            per_component_reasons[comp] = reasons

        with lock:
            sample_count[0] += 1
            for comp in components:
                if not rs_query_ok.get(comp, False):
                    rs_query_failures_per_component[comp] += 1
            for comp, n in per_component_out.items():
                prev = max_out_per_component.get(comp, 0)
                if n > prev:
                    max_out_per_component[comp] = n
                if comp not in seen_components:
                    seen_components.append(comp)
                if n > 1:
                    detail = "; ".join(per_component_reasons.get(comp, []))
                    failures.append(
                        f"VIOLATION: {comp} has {n} out-of-quorum voting members "
                        f"at sample={sample_count[0]} (cap=1) :: {detail}"
                    )
                    # Keep at most 3 first-violation reasoning lines per
                    # component for end-of-run reporting.
                    rec = flagged_reasons_per_component[comp]
                    if len(rec) < 3:
                        rec.append(
                            f"sample={sample_count[0]} n={n} :: {detail}"
                        )

    def monitor():
        while not stop_event.is_set():
            try:
                sample_once()
            except Exception as e:
                print(f"[{label}/safety-monitor] sample error: {e}")
            stop_event.wait(sample_interval_s)

    # Start legacy K8s-readiness monitor as informational diagnostic.
    stop_k8s = threading.Event()
    k8s_summary_holder: List[dict] = []

    def _k8s_wait():
        stop_k8s.wait()

    def _k8s_thread_body():
        try:
            summary = _run_per_rs_notready_monitor(
                sharded_cluster, label, _k8s_wait, timeout=timeout
            )
            k8s_summary_holder.append(summary)
        except Exception as e:
            print(f"[{label}/k8s-monitor] thread error: {e}")

    k8s_thread = threading.Thread(
        target=_k8s_thread_body, daemon=True, name=f"{label}-k8s-monitor-wrapper"
    )
    k8s_thread.start()

    monitor_thread = threading.Thread(
        target=monitor, daemon=True, name=f"{label}-safety-monitor"
    )
    monitor_thread.start()

    try:
        wait_callable()
    finally:
        stop_event.set()
        monitor_thread.join(timeout=15)
        stop_k8s.set()
        k8s_thread.join(timeout=20)

    # Quiesce check: every member of every monitored RS must be in PRIMARY
    # or SECONDARY state. This catches the "we returned to steady state"
    # requirement that's distinct from the during-test cap-1 sampling.
    #
    # The MDB CR can report Phase=Running before the LAST newly-added
    # mongod has progressed from STARTUP→SECONDARY (the operator declares
    # success when AC publishes successfully and goal-state is reached
    # by automation agents; the mongod itself catches up on a slightly
    # delayed timeline). Allow up to `quiesce_settle_s` for any DOWN
    # (state=8) / STARTUP (0/5) / RECOVERING (3) member to transition
    # to PRIMARY (1) or SECONDARY (2). Re-poll every 5s.
    quiesce_settle_s = 120
    quiesce_results: dict = {}
    quiesce_failures: List[str] = []
    quiesce_start = time.time()
    last_bad: dict = {}
    while True:
        all_clean = True
        for comp in components:
            target = _pick_ready_pod_for_component(sharded_cluster, comp)
            if target is None:
                last_bad[comp] = "no-ready-pod"
                all_clean = False
                continue
            pod_name, ns, api_client = target
            rs_status = _query_rs_status(pod_name, ns, api_client)
            if rs_status is None:
                last_bad[comp] = "rs-status-unavailable"
                all_clean = False
                continue
            states = _rs_member_states(rs_status)
            bad = [
                (name, state, health)
                for name, (state, health) in states.items()
                if state not in _RS_IN_QUORUM_STATES or health != 1
            ]
            if bad:
                last_bad[comp] = bad
                all_clean = False
            else:
                last_bad[comp] = None
                quiesce_results[comp] = f"members={len(states)} all PRIMARY/SECONDARY"
        if all_clean:
            break
        if time.time() - quiesce_start > quiesce_settle_s:
            break
        time.sleep(5)
    for comp in components:
        v = last_bad.get(comp)
        if v is None or v == "no-ready-pod" or v == "rs-status-unavailable":
            if v is None:
                continue  # already recorded as PRIMARY/SECONDARY above
            quiesce_results[comp] = v
            quiesce_failures.append(f"{comp}: {v} at quiesce (after {quiesce_settle_s}s)")
            continue
        # v is the bad-members list
        quiesce_results[comp] = f"non-quorum-members={v}"
        quiesce_failures.append(
            f"{comp}: non-quorum at quiesce after {quiesce_settle_s}s: {v}"
        )

    k8s_summary = k8s_summary_holder[0] if k8s_summary_holder else {}

    safety_summary = {
        "samples": sample_count[0],
        "components_seen": seen_components,
        "max_out_per_component": dict(max_out_per_component),
        "rs_query_failures_per_component": dict(rs_query_failures_per_component),
        "violation_reasons_per_component": {
            k: list(v) for k, v in flagged_reasons_per_component.items() if v
        },
        "quiesce": quiesce_results,
    }

    print(
        f"[{label}] SAFETY monitor (ASSERTION): {safety_summary}"
    )
    print(
        f"[{label}] k8s-readiness monitor (INFORMATIONAL): {k8s_summary}"
    )

    assert sample_count[0] > 0, (
        f"[{label}] safety monitor recorded no samples — the operation didn't "
        "give the monitor time to run"
    )
    assert not failures, (
        f"[{label}] safety violations:\n  " + "\n  ".join(failures)
    )
    assert not quiesce_failures, (
        f"[{label}] quiesce check failed:\n  " + "\n  ".join(quiesce_failures)
    )

    return {
        "safety": safety_summary,
        "k8s_readiness": k8s_summary,
    }


@mark.e2e_multi_cluster_sharded_simplest
def test_rolling_restart(sharded_cluster: MongoDB):
    """Phase G G'4: force a rolling restart and assert per-RS safety.

    Mutation: set a fresh podTemplate annotation on each component
    (configSrv, shard, mongos). This forces the operator to roll the
    underlying StatefulSets without changing version/topology/clusterSpecList
    (which would be a scale/upgrade, not a pure rolling restart).

    Safety invariant (iter-14g): across all member clusters, within each
    voting replica set (config-srv and shard-0 in this fixture), at most
    1 voting member is "out of quorum" at any time during the restart
    window. Out-of-quorum unions two signals — pod-lifecycle (Phase,
    deletionTimestamp, mongod container Running state, restartCount
    delta) and rs.status() member state (NOT in {PRIMARY, SECONDARY}).
    The previous iter-14 K8s-readiness check generated false positives
    during AutomationAgent AC reloads (mongod stayed up, but readiness
    probe flicked). Mongos is stateless and excluded from voting safety.

    Completion: MDB returns to Running with observedGeneration == generation
    within a generous timeout. The test runs in BOTH local and pod
    distributed-mode targets (it asserts a coordinator property).
    """
    print("[rolling-restart] capturing baseline generation")
    pre_generation = sharded_cluster.get_generation()
    print(f"[rolling-restart]   pre-generation = {pre_generation}")

    # Inject a fresh annotation into each component's podTemplate. The
    # operator picks this up and triggers an STS RollingUpdate.
    #
    # The CRD schema for a sharded cluster routes top-level pod-template
    # overrides through `spec.configSrvPodSpec`, `spec.shardPodSpec`, and
    # `spec.mongosPodSpec` (typed `MongoDbPodSpec`). The per-component
    # subtrees `spec.configSrv`, `spec.shard`, and `spec.mongos` are typed
    # `ShardedClusterComponentSpec` and have NO `podSpec` field — they expose
    # `additionalMongodConfig`, `agent`, and `clusterSpecList` only.
    #
    # Earlier iterations wrote the annotation to `spec.<component>.podSpec`
    # which the CRD preserves verbatim via `+kubebuilder:pruning:PreserveUnknownFields`
    # but the operator never reads. The STS template hash stayed constant and
    # no roll happened. Fix: write to the top-level `*PodSpec` paths the
    # operator actually consumes via `extractOverridesFromPodSpec` in
    # `controllers/operator/mongodbshardedcluster_controller.go`.
    trigger_value = f"rolling-restart-{int(time.time())}"
    print(f"[rolling-restart] injecting podTemplate annotation trigger={trigger_value}")
    for component_key in ("configSrvPodSpec", "shardPodSpec", "mongosPodSpec"):
        pod_spec = sharded_cluster["spec"].setdefault(component_key, {})
        pod_template = pod_spec.setdefault("podTemplate", {})
        metadata = pod_template.setdefault("metadata", {})
        annotations = metadata.setdefault("annotations", {})
        annotations["mongodb.com/rolling-restart-trigger"] = trigger_value
    sharded_cluster.update()
    if _distributed_poc_mode():
        # In distributed mode the CR has to be re-propagated to each member
        # cluster (the operators each observe their local copy only).
        do_distributed_pre_replicate(sharded_cluster)

    def _wait():
        print("[rolling-restart] waiting for MDB to return to Running with new generation")
        # iter-13c's pod-mode run finished rolling-restart in ~11 min (monitor samples
        # =331×2s=662s). iter-14's first attempt timed out at 900s mid-mongos-roll on
        # cluster-3: with the (CR, component) lease guard, all three components —
        # configSrv, shard-0, AND mongos — now serialise across the 3 member clusters.
        # mongos has 1+2+1=4 pods to roll serially (~120s/pod incl. istio sidecar),
        # so the 900s budget was tight. iter-14g local pod-mode landed at ~14 min for
        # rolling-restart; iter-14g's EVG run timed out at 1500s with the safety
        # monitor reporting clean (same max_out as local) — EVG hosts take ~25+ min
        # for the same operation (~1.7-2x slower than local). iter-14h bumps to
        # 2400s (40 min) to give EVG sufficient headroom while keeping the budget
        # closed against a runaway operator.
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)
        post_gen = sharded_cluster.get_generation()
        observed = sharded_cluster.get_status_observed_generation()
        print(f"[rolling-restart]   post-generation={post_gen} observedGeneration={observed}")
        assert post_gen is not None and post_gen > (pre_generation or 0), (
            f"generation did not advance: pre={pre_generation} post={post_gen}"
        )
        assert observed == post_gen, (
            f"observedGeneration {observed} != generation {post_gen} — operator hasn't fully reconciled"
        )

    _run_safety_monitor(sharded_cluster, "rolling-restart", _wait, timeout=2400)


@mark.e2e_multi_cluster_sharded_simplest
def test_scale_up_3(sharded_cluster: MongoDB):
    """G'5 iter 14: scale shard-0 up by 3 voting members in each cluster.

    Mutation: for each entry in `spec.shard.clusterSpecList`, increment
    `members` by 3. Starting from the test_create baseline of `[2, 2, 1]`
    for shard-0, the new shape is `[5, 5, 4]` — net +9 voting members for
    shard-0 across the three clusters.

    Safety invariant (same as rolling-restart, iter-14g): at most 1
    out-of-quorum voting member per replica set at any sample —
    measured as pod-lifecycle-not-serving OR rs-state-not-in-{PRIMARY,
    SECONDARY}, NOT K8s pod-readiness (which had false positives
    during AC reloads — iter-14f finding). The distributed coordinator's
    FSM-level `(CR, "shard-0")` lease guard (iter-13c) serialises the
    per-cluster STS scale ops so shard-0 grows one cluster at a time.
    configSrv is NOT scaled by this step — the monitor still samples
    it as a safety net (no out-of-quorum events expected).

    Completion: MDB returns to Running with a new observedGeneration.
    """
    print("[scale-up-3] capturing baseline generation")
    pre_generation = sharded_cluster.get_generation()
    print(f"[scale-up-3]   pre-generation = {pre_generation}")

    # Capture the pre-scale shard.clusterSpecList so we can compute the
    # +3-per-entry target and so test_scale_down_3 has a deterministic
    # baseline to return to (it reuses the post-create [2,2,1] shape).
    shard_spec = sharded_cluster["spec"].get("shard", {})
    pre_list = list(shard_spec.get("clusterSpecList", []))
    if not pre_list:
        raise AssertionError("test_scale_up_3 requires shard.clusterSpecList populated by test_create")
    new_list = []
    for entry in pre_list:
        new_entry = dict(entry)
        new_entry["members"] = int(new_entry.get("members", 0)) + 3
        new_list.append(new_entry)
    print(
        f"[scale-up-3] mutating shard.clusterSpecList:"
        f" pre={[ (e.get('clusterName'), e.get('members')) for e in pre_list ]}"
        f" → post={[ (e.get('clusterName'), e.get('members')) for e in new_list ]}"
    )
    sharded_cluster["spec"]["shard"]["clusterSpecList"] = new_list
    sharded_cluster.update()
    if _distributed_poc_mode():
        do_distributed_pre_replicate(sharded_cluster)

    def _wait():
        print("[scale-up-3] waiting for MDB to return to Running with new generation")
        # iter-14g local pod-mode finished scale-up-3 in ~16 min (k8s-readiness
        # samples=485×~2s≈970s). iter-14h widens to 2400s to match EVG-vs-local
        # disparity (~1.7-2x) per the iter-14g rolling-restart finding.
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)
        post_gen = sharded_cluster.get_generation()
        observed = sharded_cluster.get_status_observed_generation()
        print(f"[scale-up-3]   post-generation={post_gen} observedGeneration={observed}")
        assert post_gen is not None and post_gen > (pre_generation or 0), (
            f"generation did not advance: pre={pre_generation} post={post_gen}"
        )
        assert observed == post_gen, (
            f"observedGeneration {observed} != generation {post_gen} — operator hasn't fully reconciled"
        )

    _run_safety_monitor(sharded_cluster, "scale-up-3", _wait, timeout=2400)


@mark.e2e_multi_cluster_sharded_simplest
def test_scale_down_3(sharded_cluster: MongoDB):
    """G'5 iter 14: scale shard-0 back down by 3 voting members in each cluster.

    Inverse of test_scale_up_3 — for each entry in
    `spec.shard.clusterSpecList`, decrement `members` by 3. From the
    [5, 5, 4] post-scale-up shape this returns to the [2, 2, 1] baseline.

    Safety invariant (iter-14g): same per-RS out-of-quorum cap=1
    (pod-lifecycle ∪ rs-state), enforced by the same `(CR, "shard-0")`
    lease guard. Scale-down windows are smaller (only the trailing pod
    gets removed per reconcile cycle) but the assertion still has to
    hold: no two clusters may drop their voting members concurrently.
    """
    print("[scale-down-3] capturing baseline generation")
    pre_generation = sharded_cluster.get_generation()
    print(f"[scale-down-3]   pre-generation = {pre_generation}")

    shard_spec = sharded_cluster["spec"].get("shard", {})
    pre_list = list(shard_spec.get("clusterSpecList", []))
    if not pre_list:
        raise AssertionError("test_scale_down_3 requires shard.clusterSpecList populated by test_scale_up_3")
    new_list = []
    for entry in pre_list:
        new_entry = dict(entry)
        new_members = int(new_entry.get("members", 0)) - 3
        if new_members < 1:
            raise AssertionError(
                f"test_scale_down_3 would shrink cluster {new_entry.get('clusterName')} below 1 member "
                f"(pre={new_entry.get('members')}); did test_scale_up_3 run first?"
            )
        new_entry["members"] = new_members
        new_list.append(new_entry)
    print(
        f"[scale-down-3] mutating shard.clusterSpecList:"
        f" pre={[ (e.get('clusterName'), e.get('members')) for e in pre_list ]}"
        f" → post={[ (e.get('clusterName'), e.get('members')) for e in new_list ]}"
    )
    sharded_cluster["spec"]["shard"]["clusterSpecList"] = new_list
    sharded_cluster.update()
    if _distributed_poc_mode():
        do_distributed_pre_replicate(sharded_cluster)

    def _wait():
        print("[scale-down-3] waiting for MDB to return to Running with new generation")
        # iter-14g local pod-mode finished scale-down-3 in ~10 min. iter-14h
        # widens to 2400s for EVG-vs-local disparity headroom — scale-down
        # also serialises across clusters via the (CR, "shard-0") lease guard
        # and is similarly subject to EVG host slowdown.
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=2400)
        post_gen = sharded_cluster.get_generation()
        observed = sharded_cluster.get_status_observed_generation()
        print(f"[scale-down-3]   post-generation={post_gen} observedGeneration={observed}")
        assert post_gen is not None and post_gen > (pre_generation or 0), (
            f"generation did not advance: pre={pre_generation} post={post_gen}"
        )
        assert observed == post_gen, (
            f"observedGeneration {observed} != generation {post_gen} — operator hasn't fully reconciled"
        )

    _run_safety_monitor(sharded_cluster, "scale-down-3", _wait, timeout=2400)


# ---------------------------------------------------------------------------
# Distributed PoC helpers (only invoked when DISTRIBUTED_POC_MODE=true).
# ---------------------------------------------------------------------------


def do_distributed_setup_local(multi_cluster_operator: Operator) -> None:
    """MODE_LOCAL setup: apply CRDs to each member, verify external operator processes.

    The `multi_cluster_operator` fixture already installed the helm chart
    into the central cluster (and scaled it to 0 if LOCAL_OPERATOR=true).
    We additionally apply the static CRDs to every member cluster so the
    local operator processes can list/watch CRs there. RBAC for the
    operator itself runs out-of-cluster (kubeconfig auth) so we skip
    in-cluster ServiceAccount install for now — TODO if needed.

    The 3 operator `go run` processes must already be running externally
    (started by scripts/dev/run-3-operators-locally.sh). This helper polls
    the per-process health probe ports (8191-8193) to confirm reachability.
    """
    print("[distributed-poc/local] applying CRDs to each member cluster")
    crd_dir = "helm_chart/crds"
    if not os.path.isdir(crd_dir):
        # Tests run inside the devc with cwd=/workspace, so the path
        # resolves against /workspace.
        crd_dir = os.path.join(os.environ.get("WORKSPACE", "/workspace"), "helm_chart/crds")
    if not os.path.isdir(crd_dir):
        raise FileNotFoundError(f"CRDs dir not found: {crd_dir}")

    for cluster in get_member_cluster_names():
        # Match the per-cluster kubeconfig filename produced by D'2:
        # .generated/cluster-{1,2,3}.kubeconfig. The stem is the cluster
        # name with the "kind-e2e-" prefix dropped.
        stem = cluster.removeprefix("kind-e2e-")
        kc = os.path.join(os.environ.get("WORKSPACE", "/workspace"), ".generated", f"{stem}.kubeconfig")
        if not os.path.isfile(kc):
            raise FileNotFoundError(
                f"per-cluster kubeconfig missing: {kc}\n"
                "Run scripts/dev/extract_member_kubeconfigs.sh first."
            )
        print(f"[distributed-poc]   {cluster}: kubectl apply -f {crd_dir}")
        subprocess.run(
            ["kubectl", "--kubeconfig", kc, "apply", "-f", crd_dir],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    print("[distributed-poc] verifying operator processes are reachable")
    # A `go run` cold start takes 30-60s before the health probe binds.
    # Poll up to 120s per port so reruns against a freshly-restarted
    # set of operators don't race the test fixture.
    import socket
    import time as _time

    deadline_per_port = 120
    for idx, _ in enumerate(get_member_cluster_names()):
        port = 8191 + idx
        started = _time.monotonic()
        last_err: OSError | None = None
        while _time.monotonic() - started < deadline_per_port:
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.settimeout(2)
            try:
                s.connect(("127.0.0.1", port))
                s.close()
                print(f"[distributed-poc]   health probe 127.0.0.1:{port} reachable")
                break
            except OSError as e:
                last_err = e
                s.close()
                _time.sleep(2)
        else:
            raise RuntimeError(
                f"operator health probe 127.0.0.1:{port} not reachable after {deadline_per_port}s: {last_err}\n"
                "Did scripts/dev/run-3-operators-locally.sh run successfully?"
            )


def do_distributed_setup_pod(multi_cluster_operator: Operator) -> None:
    """MODE_POD setup: helm-install the operator chart into each member cluster.

    Each member cluster gets its own Deployment + ServiceAccount + RBAC +
    Service exposing the raft (tcp-raft / 7000) and proposal-forwarder
    (tcp-raftapp / 7001) ports. The 3 pods coordinate via Raft over the
    Istio multi-cluster mesh — each pod connects to its peers' DNS names
    (mongodb-kubernetes-operator-raft-<stem>.<ns>.svc.cluster.local:7000
    for raft heartbeats; +1 for forwarded proposals).

    The `multi_cluster_operator` fixture installed the chart into the central
    cluster as usual. In distributed pod-mode we leave that install in place
    but scale it to 0 (no pod) — it's only useful because it created the
    namespace-scoped objects (operator-installation-config ConfigMap) that
    this helper reads to discover the registry overrides.

    Image source: this helper reads the central installation config to
    inherit the image registry + version that the e2e harness was set up
    with (typically OVERRIDE_VERSION_ID-tagged images pushed to ECR by an
    EVG `init_test_run` task).
    """
    print("[distributed-poc/pod] starting in-pod operator installation")
    ws = os.environ.get("WORKSPACE", "/workspace")
    chart_path = os.path.join(ws, "helm_chart")
    if not os.path.isdir(chart_path):
        raise FileNotFoundError(f"helm chart dir not found: {chart_path}")

    members = list(get_member_cluster_names())
    if len(members) < 1:
        raise RuntimeError("no member clusters configured for distributed setup")

    # Build the deterministic peers list. All 3 operators see the same string;
    # each operator's RAFT_CLUSTER_NAME pins it to its own entry. The peers
    # entries advertise each operator's RAFT port (7000); the proposal-
    # forwarder app port (7001) is derived by the operator at dial time
    # (AppPortFromRaftAddr — port + 1). The Service exposes both ports under
    # named `tcp-raft` / `tcp-raftapp` for Istio passthrough.
    raft_port = 7000
    op_name = "mongodb-kubernetes-operator"

    def _stem(cluster: str) -> str:
        return cluster.removeprefix("kind-e2e-")

    namespace = multi_cluster_operator.namespace
    peers = ",".join(
        f"{c}={op_name}-raft-{_stem(c)}.{namespace}.svc.cluster.local:{raft_port}"
        for c in members
    )
    print(f"[distributed-poc/pod] peers={peers}")

    # Scale the central install to 0 so it doesn't compete with the member
    # operators. multi_cluster_operator was created in the central cluster.
    try:
        central_api = k8s_client.AppsV1Api(api_client=multi_cluster_operator.api_client)
        central_api.patch_namespaced_deployment_scale(
            namespace=namespace, name=multi_cluster_operator.name,
            body={"spec": {"replicas": 0}},
        )
        print("[distributed-poc/pod]   scaled central operator deployment to 0")
    except k8s_client.exceptions.ApiException as e:
        print(f"[distributed-poc/pod]   warning: could not scale central operator: {e}")

    # Read the central operator-installation-config ConfigMap to inherit
    # registry overrides. The keys we care about: registry.*, operator.version,
    # operator.build, agent.version, database.version, initDatabase.version,
    # initOpsManager.version, registry.imagePullSecrets.
    from kubetester.kubetester import KubernetesTester
    try:
        install_cfg = KubernetesTester.read_configmap(
            namespace, "operator-installation-config",
            api_client=multi_cluster_operator.api_client,
        )
    except k8s_client.exceptions.ApiException as e:
        raise RuntimeError(
            f"operator-installation-config not found in central cluster: {e}\n"
            "Run `make prepare-local-e2e` before invoking pod-mode."
        )

    # Whitelist the helm values we propagate — we don't want to leak local-only
    # toggles like operator.replicas=0 to the member-cluster installs.
    propagated_keys = {
        "registry.imagePullSecrets",
        "registry.operator",
        "registry.database",
        "registry.initDatabase",
        "registry.opsManager",
        "registry.initOpsManager",
        "registry.agent",
        "operator.version",
        "operator.build",
        "operator.operator_image_name",
        "operator.mdbDefaultArchitecture",
        "agent.version",
        "database.version",
        "initDatabase.version",
        "initOpsManager.version",
        "managedSecurityContext",
        "customEnvVars",
    }
    base_values = {k: v for k, v in install_cfg.items() if k in propagated_keys}
    print(f"[distributed-poc/pod] inherited base helm values: {sorted(base_values.keys())}")

    # iter-17a fix: distributed pod operators must know the full cluster
    # membership topology. Hub-spoke gets this via two artefacts that
    # `multi-cluster-kube-config-creator` writes into the CENTRAL cluster:
    #   - ConfigMap `mongodb-kubernetes-operator-member-list` (key per cluster name).
    #   - Secret `mongodb-enterprise-operator-multi-cluster-kubeconfig` (kubeconfig blob).
    # Without these on member clusters, each pod-mode operator only sees its
    # own cluster + `__default` in `globalMemberClustersMap`, so it treats
    # peer clusters as "down" and writes 0-replica STS specs that the pod
    # controller then converts into deletions on the peer clusters. The fix:
    # replicate both artefacts into each member cluster's namespace, and
    # set `multiCluster.clusters` on each helm install so the chart wires
    # `-watch-resource=mongodbmulticluster` (which causes main.go to invoke
    # `getMemberClusters` against the CM at startup). The MultiCluster
    # controller registration itself is suppressed in distributed mode
    # (main.go gates it on `distributedCoordinator != nil`), so adding the
    # watch flag is benign — it only enables the CM read path.
    members_clusters_value = ",".join(members)  # e.g. "kind-e2e-cluster-1,kind-e2e-cluster-2,kind-e2e-cluster-3"
    mc_clusters_helm_value = "{" + members_clusters_value + "}"  # helm array syntax

    # Read CM + Secret from the central cluster — these are produced by
    # `make prepare-local-e2e`'s `multi-cluster-kube-config-creator multicluster setup`.
    central_core = k8s_client.CoreV1Api(api_client=multi_cluster_operator.api_client)
    try:
        central_member_list_cm = central_core.read_namespaced_config_map(
            name="mongodb-kubernetes-operator-member-list", namespace=namespace,
        )
    except k8s_client.exceptions.ApiException as e:
        raise RuntimeError(
            f"central member-list CM not found in {namespace}: {e}\n"
            "Run `make prepare-local-e2e` first so multi-cluster-kube-config-creator "
            "creates the CM."
        )
    try:
        central_mc_kubeconfig_secret = central_core.read_namespaced_secret(
            name="mongodb-enterprise-operator-multi-cluster-kubeconfig", namespace=namespace,
        )
    except k8s_client.exceptions.ApiException as e:
        raise RuntimeError(
            f"central multi-cluster kubeconfig Secret not found in {namespace}: {e}\n"
            "Run `make prepare-local-e2e` first."
        )
    member_list_data = central_member_list_cm.data or {}
    mc_kubeconfig_data = central_mc_kubeconfig_secret.data or {}
    print(f"[distributed-poc/pod] central member-list CM has keys: {sorted(member_list_data.keys())}")

    for idx, cluster in enumerate(members):
        kc = os.path.join(ws, ".generated", f"{_stem(cluster)}.kubeconfig")
        if not os.path.isfile(kc):
            raise FileNotFoundError(
                f"per-cluster kubeconfig missing: {kc}\n"
                "Run scripts/dev/extract_member_kubeconfigs.sh first."
            )

        # CRDs first — the helm chart doesn't ship them inline.
        crd_dir = os.path.join(ws, "helm_chart/crds")
        print(f"[distributed-poc/pod]   {cluster}: kubectl apply -f {crd_dir}")
        # Use server-side apply with force-conflicts so existing CRDs (e.g.
        # left over from a prior iter) don't break the install — the
        # client-side apply path raises AlreadyExists for objects whose
        # last-applied-configuration annotation predates the current YAML.
        crd_res = subprocess.run(
            ["kubectl", "--kubeconfig", kc, "apply",
             "--server-side", "--force-conflicts", "-f", crd_dir],
            check=False, capture_output=True, text=True,
        )
        if crd_res.returncode != 0:
            print(f"[distributed-poc/pod]   {cluster}: CRD apply stdout:\n{crd_res.stdout}")
            print(f"[distributed-poc/pod]   {cluster}: CRD apply stderr:\n{crd_res.stderr}")
            raise subprocess.CalledProcessError(
                crd_res.returncode,
                ["kubectl", "apply", "--server-side", "--force-conflicts", "-f", crd_dir],
                output=crd_res.stdout, stderr=crd_res.stderr,
            )

        # iter-17a fix: replicate central CM + Secret to this member cluster
        # so its operator's startup `getMemberClusters` returns the full
        # list of cluster names, and the chart's kubeconfig volume mount
        # has a backing Secret.
        for _attempt in range(3):
            try:
                # Two-step: render CM YAML via dry-run, then apply (idempotent).
                rendered = subprocess.run(
                    ["kubectl", "--kubeconfig", kc, "-n", namespace,
                     "create", "configmap", "mongodb-kubernetes-operator-member-list",
                     *sum([["--from-literal", f"{k}={v}"] for k, v in member_list_data.items()], []),
                     "--dry-run=client", "-o", "yaml"],
                    check=True, capture_output=True, text=True,
                )
                subprocess.run(
                    ["kubectl", "--kubeconfig", kc, "-n", namespace, "apply", "-f", "-"],
                    input=rendered.stdout, check=True, capture_output=True, text=True,
                )
                # Apply Secret similarly.
                # The Secret data values are already base64 from the API; we
                # build a Secret manifest directly.
                secret_yaml_lines = [
                    "apiVersion: v1",
                    "kind: Secret",
                    "metadata:",
                    f"  name: mongodb-enterprise-operator-multi-cluster-kubeconfig",
                    f"  namespace: {namespace}",
                    f"type: {central_mc_kubeconfig_secret.type or 'Opaque'}",
                    "data:",
                ]
                for k, v in mc_kubeconfig_data.items():
                    secret_yaml_lines.append(f"  {k}: {v}")
                secret_yaml = "\n".join(secret_yaml_lines) + "\n"
                subprocess.run(
                    ["kubectl", "--kubeconfig", kc, "-n", namespace, "apply", "-f", "-"],
                    input=secret_yaml, check=True, capture_output=True, text=True,
                )
                print(f"[distributed-poc/pod]   {cluster}: replicated member-list CM + mc-kubeconfig Secret")
                break
            except subprocess.CalledProcessError as e:
                print(f"[distributed-poc/pod]   {cluster}: replicate attempt {_attempt+1} failed: {e.stderr}")
                if _attempt == 2:
                    raise
                time.sleep(2)

        # Build helm --set args.
        bootstrap = "true" if idx == 0 else "false"
        # Helm --set splits on commas — escape them in the peers string.
        peers_escaped = peers.replace(",", "\\,")
        # Keys whose chart template does a strict bool comparison (eq … true).
        # These must NOT go through --set-string (which yields a string
        # "false" that fails `eq … true` with "incompatible types for
        # comparison: string and bool").
        BOOL_KEYS = {"managedSecurityContext"}
        set_args: List[str] = []
        for k, v in base_values.items():
            # operator-installation-config bashes special chars (& is the
            # separator for customEnvVars). Pass the raw values verbatim,
            # but use --set (bool-typed) for keys we know are bools.
            flag = "--set" if k in BOOL_KEYS else "--set-string"
            set_args.extend([flag, f"{k}={v}"])
        # iter-17a fix: pass `multiCluster.clusters` as a comma-list helm
        # array. This unlocks two chart paths needed for distributed pod-mode
        # parity with hub-spoke:
        #   (a) adds `-watch-resource=mongodbmulticluster` so the operator
        #       binary invokes `getMemberClusters()` against the CM at startup.
        #   (b) mounts the mc-kubeconfig Secret at /etc/config/kubeconfig
        #       which `getKubeConfigPath()` reads.
        # The MongoDBMultiCluster controller registration itself is suppressed
        # in distributed mode (main.go gates it on `distributedCoordinator != nil`).
        mc_clusters_escaped = mc_clusters_helm_value.replace(",", "\\,")
        set_args.extend([
            "--set", "operator.distributed.enabled=true",
            "--set-string", f"operator.distributed.clusterName={cluster}",
            "--set-string", f"operator.distributed.peers={peers_escaped}",
            "--set", f"operator.distributed.bootstrap={bootstrap}",
            "--set", "operator.replicas=1",
            "--set", f"multiCluster.clusters={mc_clusters_escaped}",
            # Webhook registration stays ENABLED (chart default). With it
            # disabled the operator binary still wires the per-CRD validating
            # webhooks into controller-runtime, which then tries to serve
            # them over TLS and fails fatally on missing
            # /tmp/k8s-webhook-server/serving-certs/tls.crt. The operator
            # creates a self-signed cert at startup when
            # registerConfiguration=true; the bundled ClusterRole that's
            # gated on `installClusterRole` is the only cross-cluster bit,
            # which the operator ServiceAccount needs to manage the
            # ValidatingWebhookConfiguration. Per-member ClusterRole is OK
            # here because each member operator is a self-contained
            # installation.
            # prepare-multi-cluster already created the appdb/database-pods/
            # ops-manager ServiceAccounts + Roles in each member namespace.
            # Suppress the chart's copies to avoid helm "exists and cannot be
            # imported" ownership-metadata errors.
            "--set", "operator.createResourcesServiceAccountsAndRoles=false",
        ])
        helm_cmd = [
            "helm", "upgrade", "--install",
            f"--kubeconfig={kc}",
            f"--namespace={namespace}",
            "--create-namespace",
            *set_args,
            "mongodb-kubernetes-operator", chart_path,
        ]
        print(f"[distributed-poc/pod]   {cluster}: helm upgrade --install")
        # Clear KUBECONFIG env so helm only reads --kubeconfig. The test
        # process inherits KUBECONFIG=/workspace/.generated/current.devc.kubeconfig
        # which under some helm versions overrides the flag and causes empty-output failures.
        helm_env = os.environ.copy()
        helm_env.pop("KUBECONFIG", None)
        helm_env.pop("HELM_KUBECONTEXT", None)
        # Also write helm output to a per-cluster file so it survives pytest capture.
        helm_log = os.path.join(ws, "logs", f"helm-pod-{_stem(cluster)}.log")
        os.makedirs(os.path.dirname(helm_log), exist_ok=True)
        with open(helm_log, "w") as fh:
            res = subprocess.run(helm_cmd, env=helm_env, stdout=fh, stderr=subprocess.STDOUT)
        with open(helm_log) as fh:
            helm_out = fh.read()
        print(f"[distributed-poc/pod]   {cluster}: helm output (rc={res.returncode}, log={helm_log}):\n{helm_out}")
        if res.returncode != 0:
            raise subprocess.CalledProcessError(
                res.returncode, helm_cmd, output=helm_out, stderr=helm_out,
            )

    # Wait for each member's operator Deployment to become Available.
    # Per-cluster deadline so a slow image-pull on one cluster doesn't
    # eat the budget for the others. First-time ECR pulls + go binary
    # cold start + raft peer rendezvous easily breach the original 240s
    # global budget.
    print("[distributed-poc/pod] waiting for member operator Deployments to be Available")
    for cluster in members:
        kc = os.path.join(ws, ".generated", f"{_stem(cluster)}.kubeconfig")
        deadline = time.monotonic() + 600
        while True:
            if time.monotonic() >= deadline:
                raise RuntimeError(
                    f"timed out waiting (600s) for operator Deployment to be Available in {cluster}"
                )
            res = subprocess.run(
                ["kubectl", "--kubeconfig", kc, "-n", namespace,
                 "get", "deployment", op_name,
                 "-o", "jsonpath={.status.conditions[?(@.type=='Available')].status}"],
                capture_output=True, text=True,
            )
            if res.returncode == 0 and res.stdout.strip() == "True":
                print(f"[distributed-poc/pod]   {cluster}: operator Available")
                break
            time.sleep(3)


def do_distributed_pre_replicate(sharded_cluster: MongoDB) -> None:
    """Replicate spec-referenced resources + MDB CR to each member cluster.

    Calls scripts/dev/replicate_cr_resources.sh which copies the project
    ConfigMap + credentials Secret (+ any TLS / auth secrets) from the
    central kubeconfig to each per-cluster kubeconfig. Then explicitly
    applies the MDB CR YAML to every member cluster so all local operators
    observe identical specs.
    """
    print("[distributed-poc] replicating spec-referenced resources to all members")
    ws = os.environ.get("WORKSPACE", "/workspace")
    script = os.path.join(ws, "scripts/dev/replicate_cr_resources.sh")
    if not os.path.isfile(script):
        raise FileNotFoundError(f"replicate script missing: {script}")
    subprocess.run(
        [script, sharded_cluster.namespace, sharded_cluster.name],
        check=True,
        cwd=ws,
    )

    # Replicate the MDB CR itself. The python kubetester writes via the
    # central client; we copy the CR object to each member cluster's
    # mongodb.com/v1 MongoDB resource.
    print("[distributed-poc] propagating MDB CR to each member cluster")
    central_body = sharded_cluster.backing_obj
    api_group = "mongodb.com"
    api_version = "v1"
    plural = "mongodb"
    namespace = sharded_cluster.namespace
    name = sharded_cluster.name
    first_member_api_client = None
    for cluster in get_member_cluster_names():
        stem = cluster.removeprefix("kind-e2e-")
        kc = os.path.join(ws, ".generated", f"{stem}.kubeconfig")

        # Build a per-cluster ApiClient honouring the kubeconfig's proxy-url
        # (gost-proxy from inside the devcontainer). load_kube_config alone
        # ignores proxy-url. The kubetester helper load_proxy_config patches
        # the Configuration to use the proxy.
        merger = k8s_config.kube_config.KubeConfigMerger(kc)
        configuration = k8s_client.Configuration()
        k8s_config.load_kube_config_from_dict(merger.config, client_configuration=configuration)
        load_proxy_config(merger.config, configuration)
        api_client = k8s_client.ApiClient(configuration=configuration)
        if first_member_api_client is None:
            first_member_api_client = api_client
        co = k8s_client.CustomObjectsApi(api_client=api_client)
        try:
            existing = co.get_namespaced_custom_object(
                group=api_group, version=api_version, namespace=namespace, plural=plural, name=name
            )
            body = dict(central_body)
            body["metadata"] = {"name": name, "namespace": namespace,
                                "resourceVersion": existing["metadata"]["resourceVersion"]}
            body["spec"] = central_body["spec"]
            co.replace_namespaced_custom_object(
                group=api_group, version=api_version, namespace=namespace,
                plural=plural, name=name, body=body,
            )
            print(f"[distributed-poc]   updated MDB CR in {cluster}")
        except k8s_client.exceptions.ApiException as e:
            if e.status != 404:
                raise
            body = {"apiVersion": f"{api_group}/{api_version}", "kind": "MongoDB",
                    "metadata": {"name": name, "namespace": namespace},
                    "spec": central_body["spec"]}
            co.create_namespaced_custom_object(
                group=api_group, version=api_version, namespace=namespace,
                plural=plural, body=body,
            )
            print(f"[distributed-poc]   created MDB CR in {cluster}")

    # Rebind sharded_cluster's CustomObjectsApi to a member cluster so the
    # subsequent assert_reaches_phase polls the status reported by an
    # operator that is actually running (the central cluster has no
    # operator pod in distributed mode and never updates the MDB status
    # there).
    if first_member_api_client is not None:
        sharded_cluster.api = k8s_client.CustomObjectsApi(api_client=first_member_api_client)
        print("[distributed-poc] sharded_cluster.api rebound to first member cluster client")
