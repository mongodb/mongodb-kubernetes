"""
SECBUG archetype-A empirical proof (RecoverPanic).

Aegis flagged a family of "untrusted CR field -> operator panic -> cluster-wide
CrashLoopBackOff DoS" findings (e.g. SECBUG-4085: negative shardCount reaches
`make([]string, m.Spec.ShardCount)` and panics). This test settles the *impact*
question empirically for the whole family:

controller-runtime v0.20.4 defaults `RecoverPanic=true`, and MCK sets it nowhere
(every controller registers bare `controller.Options{Reconciler, MaxConcurrentReconciles}`;
`main.go` sets no manager-level Controller config). So a panic on the Reconcile path
is recovered + requeued and the operator process does NOT die.

We apply a MongoDB sharded-cluster CR with `shardCount: -1` (admitted: the CRD has no
`minimum` and the webhook's `shardCountSpecified` only rejects `== 0`). The reconcile
connects to the project and then panics at `createOrUpdateShards`
(`make([]string, -1)`), which controller-runtime recovers on every loop. We assert:

  1. the operator pod is NOT recreated and its container restart_count stays 0
     (no process crash / CrashLoopBackOff) across a sustained window,
  2. the operator logs carry the recovered-panic signal (a panic really happened
     AND was recovered),
  3. the malicious resource never reaches Running (it hot-loops, self-scoped).

Together these downgrade the "cluster-wide operator DoS" claim to a single-resource
requeue loop. Blast-radius containment follows from (1): the process stays up and
keeps serving its other watched resources.
"""

import time

from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# Emitted by controller-runtime's default panic handling / the recovered-error string.
PANIC_LOG_SIGNALS = ["[recovered]", "Observed a panic", "makeslice: len out of range"]
# How long to watch the operator while the malicious CR hot-loops.
OBSERVE_SECONDS = 90
OBSERVE_INTERVAL = 5
# How long to wait for the recovered-panic signal to appear (absorbs slow project connect).
PANIC_LOG_TIMEOUT = 180


@fixture(scope="module")
def malicious_sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), "sh-negative-shardcount", namespace)
    resource["spec"]["shardCount"] = -1
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    try_load(resource)
    return resource


def _operator_pod(operator: Operator):
    pods = operator.list_operator_pods()
    assert len(pods) == 1, f"expected exactly one operator pod, got {len(pods)}"
    return pods[0]


def _restart_count(pod) -> int:
    return sum(cs.restart_count for cs in (pod.status.container_statuses or []))


@mark.e2e_operator_reconcile_panic_liveness
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_operator_reconcile_panic_liveness
class TestReconcilePanicIsRecovered:
    def test_operator_baseline_healthy(self, operator: Operator):
        pod = _operator_pod(operator)
        assert pod.status.phase == "Running"
        assert _restart_count(pod) == 0

    def test_apply_malicious_cr(self, malicious_sc: MongoDB):
        # -1 passes the CRD (no minimum) and the webhook (shardCountSpecified rejects only == 0).
        malicious_sc.update()

    def test_operator_survives_reconcile_panics(self, operator: Operator, malicious_sc: MongoDB):
        # The reconcile panics each loop and is recovered by controller-runtime.
        # The operator process must neither be recreated nor restart its container.
        baseline_pod_name = _operator_pod(operator).metadata.name
        deadline = time.time() + OBSERVE_SECONDS
        while time.time() < deadline:
            pod = _operator_pod(operator)
            assert pod.metadata.name == baseline_pod_name, "operator pod was recreated (process crashed)"
            assert _restart_count(pod) == 0, "operator container restarted (CrashLoopBackOff)"
            assert pod.status.phase == "Running", f"operator pod not Running: {pod.status.phase}"
            time.sleep(OBSERVE_INTERVAL)
        logger.info(f"Operator survived {OBSERVE_SECONDS}s of recovered reconcile panics without restarting")

    def test_recovered_panic_is_logged(self, operator: Operator, namespace: str):
        # Poll: the panic is emitted once the reconcile reaches createOrUpdateShards after
        # connecting to the project; polling absorbs transient connect latency. Throughout,
        # the operator must still not have restarted.
        deadline = time.time() + PANIC_LOG_TIMEOUT
        last_logs = ""
        while time.time() < deadline:
            pod = _operator_pod(operator)
            assert _restart_count(pod) == 0, "operator container restarted (CrashLoopBackOff)"
            last_logs = KubernetesTester.read_pod_logs(namespace, pod.metadata.name)
            if any(sig in last_logs for sig in PANIC_LOG_SIGNALS):
                logger.info("Observed recovered-panic signal in operator logs")
                return
            time.sleep(OBSERVE_INTERVAL)
        raise AssertionError(
            f"no recovered-panic signal (one of {PANIC_LOG_SIGNALS}) in operator logs within "
            f"{PANIC_LOG_TIMEOUT}s; the panic path may not have been reached"
        )

    def test_malicious_cr_never_reaches_running(self, malicious_sc: MongoDB):
        # The panic happens before the reconcile sets a terminal status, so the resource
        # never reaches Running; it stays self-scoped and hot-loops.
        malicious_sc.load()
        assert malicious_sc.get_status_phase() != Phase.Running
