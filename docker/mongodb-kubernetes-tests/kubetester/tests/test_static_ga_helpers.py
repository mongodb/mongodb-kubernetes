"""Regression tests for the generic readiness helper and the operator resolution helper.

Each test covers one defect that a #8863 e2e task hit: a component with zero members, pod selection by name prefix
instead of ownership, the single readiness read after the wait, and the Vault container count. The last section
pins the boundary: the shared helpers perform no architecture, topology or proxy acceptance.
"""

from types import SimpleNamespace
from typing import cast
from unittest.mock import MagicMock

import kubernetes.client
import pytest
from kubetester.kubetester import (
    KubernetesTester,
    assert_database_pod_container_count,
    assert_no_statefulset_pods,
    automation_process_name,
    released_agent_config,
    statefulset_pod_selector,
    statefulset_pods,
    with_custom_env_var,
)
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase

NAMESPACE = "test-namespace"
RESOURCE = "mdb-rs"


def object_meta(name, uid="sts-uid", owner_name=None, owner_uid=None, deletion_timestamp=None):
    owner = None
    if owner_name is not None:
        owner = kubernetes.client.V1OwnerReference(
            api_version="apps/v1",
            kind="StatefulSet",
            name=owner_name,
            uid=owner_uid or uid,
            controller=True,
        )
    return kubernetes.client.V1ObjectMeta(
        name=name,
        uid=uid,
        owner_references=[owner] if owner else None,
        deletion_timestamp=deletion_timestamp,
    )


def pod(name, ready, uid="sts-uid", deletion_timestamp=None, owner_name=None, owner_uid=None):
    owner_name = owner_name or name.rsplit("-", 1)[0]
    meta = object_meta(
        name,
        uid=uid,
        owner_name=owner_name,
        owner_uid=owner_uid or uid,
        deletion_timestamp=deletion_timestamp,
    )
    meta.labels = {"app": f"{owner_name}-svc"}
    return kubernetes.client.V1Pod(
        metadata=meta,
        status=kubernetes.client.V1PodStatus(
            phase="Running",
            conditions=[kubernetes.client.V1PodCondition(type="Ready", status="True" if ready else "False")],
        ),
    )


def fake_core(pods):
    """A CoreV1Api double that filters by the requested label selector, like the API server does."""
    core = MagicMock()

    def list_namespaced_pod(namespace, label_selector=None):
        return SimpleNamespace(items=[p for p in pods if pod_matches(p, label_selector)])

    core.list_namespaced_pod.side_effect = list_namespaced_pod
    return core


def pod_matches(pod, label_selector):
    labels = pod.metadata.labels or {}
    if not label_selector:
        return True
    return all(labels.get(key) == value for key, value in (pair.split("=", 1) for pair in label_selector.split(",")))


def statefulset(name, replicas, uid="sts-uid"):
    return kubernetes.client.V1StatefulSet(
        metadata=object_meta(name, uid=uid),
        spec=kubernetes.client.V1StatefulSetSpec(
            replicas=replicas,
            service_name=f"{name}-svc",
            selector=kubernetes.client.V1LabelSelector(match_labels={"app": f"{name}-svc"}),
            template=kubernetes.client.V1PodTemplateSpec(),
        ),
    )


# --- pod selection -------------------------------------------------------------------------------------------


def test_pod_selector_uses_the_statefulset_labels():
    assert statefulset_pod_selector(statefulset("mdb-rs", 3)) == "app=mdb-rs-svc"


def test_statefulset_pods_excludes_similarly_named_workloads(monkeypatch):
    """A MongoDBSearch, Envoy or metrics pod shares the name prefix but not the StatefulSet selector."""
    sts = statefulset("mdb-rs", 3)
    search_pod = pod("mdb-rs-search-0", True)
    search_pod.metadata.labels = {"app": "mdb-rs-search"}
    owned = pod("mdb-rs-0", True)
    foreign = pod("mdb-rs-1", True, uid="other-uid")

    core = fake_core([search_pod, owned, foreign])
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)

    selected = statefulset_pods(sts, NAMESPACE)
    assert [p.metadata.name for p in selected] == ["mdb-rs-0"]
    assert core.list_namespaced_pod.call_args.kwargs["label_selector"] == "app=mdb-rs-svc"


def test_statefulset_pods_excludes_a_sibling_resource_with_the_same_prefix(monkeypatch):
    """replica-set-2 pods start with "replica-set-", so a prefix alone must never select them."""
    sts = statefulset("replica-set", 3)
    sibling = pod("replica-set-2-0", True, uid="sibling-uid", owner_name="replica-set-2")
    owned = [pod(f"replica-set-{i}", True) for i in range(3)]

    core = fake_core([sibling, *owned])
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)

    selected = statefulset_pods(sts, NAMESPACE)
    assert [p.metadata.name for p in selected] == ["replica-set-0", "replica-set-1", "replica-set-2"]


# --- zero members --------------------------------------------------------------------------------------------


def test_zero_member_component_accepts_absence(monkeypatch):
    core = MagicMock()
    core.list_namespaced_pod.return_value.items = []
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    assert_no_statefulset_pods(NAMESPACE, r"mdb-rs-config")


def test_zero_member_component_rejects_a_stale_pod(monkeypatch):
    stale = pod("mdb-rs-config-0", True, uid="deleted-sts")
    stale.metadata.owner_references[0].name = "mdb-rs-config"
    core = MagicMock()
    core.list_namespaced_pod.return_value.items = [stale]
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    with pytest.raises(AssertionError):
        assert_no_statefulset_pods(NAMESPACE, r"mdb-rs-config", timeout=1, sleep_time=1)


def test_zero_member_component_accepts_a_terminating_pod(monkeypatch):
    terminating = pod("mdb-rs-config-0", True, deletion_timestamp="2026-11-05T10:00:00Z")
    terminating.metadata.owner_references[0].name = "mdb-rs-config"
    core = MagicMock()
    core.list_namespaced_pod.return_value.items = [terminating]
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    assert_no_statefulset_pods(NAMESPACE, r"mdb-rs-config")


# --- readiness transition ------------------------------------------------------------------------------------


def test_pods_helper_polls_past_a_transient_not_ready_state(monkeypatch):
    """The wait used to return, and a single later read then caught the agent mid-transition."""
    sts = statefulset("mdb-rs", 1)
    snapshots = [
        [pod("mdb-rs-0", False)],
        [pod("mdb-rs-0", False)],
        [pod("mdb-rs-0", True)],
    ]
    calls = {"n": 0}

    def next_snapshot(*args, **kwargs):
        index = min(calls["n"], len(snapshots) - 1)
        calls["n"] += 1
        return snapshots[index]

    monkeypatch.setattr("kubetester.kubetester.statefulset_pods", next_snapshot)
    KubernetesTester.assert_static_ga_pods(sts, NAMESPACE, database_running=True)
    assert calls["n"] >= 3, "the helper must poll the readiness condition instead of reading it once"


def test_pods_helper_keeps_the_transient_state_failure(monkeypatch):
    sts = statefulset("mdb-rs", 1)
    monkeypatch.setattr("kubetester.kubetester.statefulset_pods", lambda *a, **k: [pod("mdb-rs-0", False)])
    with pytest.raises(AssertionError):
        KubernetesTester.assert_static_ga_pods(sts, NAMESPACE, database_running=True, timeout=1)


def test_pods_helper_rejects_an_extra_running_pod(monkeypatch):
    sts = statefulset("mdb-rs", 1)
    monkeypatch.setattr(
        "kubetester.kubetester.statefulset_pods",
        lambda *a, **k: [pod("mdb-rs-0", True), pod("mdb-rs-1", True)],
    )
    with pytest.raises(AssertionError):
        KubernetesTester.assert_static_ga_pods(sts, NAMESPACE, database_running=True, timeout=1)


def test_pods_helper_checks_absence_for_a_zero_replica_statefulset(monkeypatch):
    sts = statefulset("mdb-rs", 0)
    called = {}
    monkeypatch.setattr(
        "kubetester.kubetester.assert_no_statefulset_pods",
        lambda namespace, pattern, **k: called.update(namespace=namespace, pattern=pattern),
    )
    KubernetesTester.assert_static_ga_pods(sts, NAMESPACE, database_running=True)
    assert called == {"namespace": NAMESPACE, "pattern": "mdb\\-rs"}


# --- Vault container counts ----------------------------------------------------------------------------------


@pytest.mark.parametrize(
    "architecture,expected",
    [("static", 4), ("static-ga", 3), ("non-static", 2)],
)
def test_database_pod_container_count_per_architecture(monkeypatch, architecture, expected):
    monkeypatch.setenv("MDB_DEFAULT_ARCHITECTURE", architecture)
    assert_database_pod_container_count(expected)
    with pytest.raises(AssertionError):
        assert_database_pod_container_count(expected + 1)


# --- operator resolution -------------------------------------------------------------------------------------


def installation_config():
    return {
        "operator.mdbDefaultArchitecture": "static-ga",
        "registry.operator": "quay.io/mongodb",
        "operator.version": "1.15.1",
        "agent.customAgentUrl": "https://example.invalid/mongodb-mms-automation-agent-110.0.0.9423-1.linux_x86_64.tar.gz",
        "registry.mongodbProxy": "quay.io/mongodb",
        "mongodbProxy.name": "mongodb-kubernetes-mongodb-proxy",
        "mongodbProxy.version": "1.15.1",
    }


def test_operator_resolution_reads_the_resource_namespace(monkeypatch):
    """An MCK-to-MCK migration runs the target operator in the target namespace."""
    monkeypatch.setenv("MDB_DEFAULT_ARCHITECTURE", "static-ga")
    monkeypatch.setenv("NAMESPACE", "source-ns")
    core = MagicMock()
    core.read_namespaced_config_map.return_value.data = installation_config()
    apps = MagicMock()
    apps.list_namespaced_deployment.return_value.items = []
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    monkeypatch.setattr(kubernetes.client, "AppsV1Api", lambda api_client=None: apps)
    monkeypatch.setattr(
        KubernetesTester,
        "_active_olm_deployments",
        staticmethod(lambda namespace, api_client=None: {}),
    )
    monkeypatch.setattr(
        "kubetester.kubetester.validate_static_ga_config",
        lambda config: None,
    )

    with pytest.raises(AssertionError, match="active current or historical operator"):
        # The target namespace has the config but no operator deployment, so convergence is unproven.
        KubernetesTester.static_ga_config_for_current_operator(namespace="target-ns")

    core.read_namespaced_config_map.assert_called_once_with("operator-installation-config", "target-ns")
    apps.list_namespaced_deployment.assert_called_once_with("target-ns")


def test_operator_resolution_falls_back_to_the_operator_namespace(monkeypatch):
    monkeypatch.setenv("MDB_DEFAULT_ARCHITECTURE", "static-ga")
    monkeypatch.setenv("NAMESPACE", "operator-ns")
    core = MagicMock()
    core.read_namespaced_config_map.side_effect = kubernetes.client.exceptions.ApiException(status=404)
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    monkeypatch.setattr(kubernetes.client, "AppsV1Api", lambda api_client=None: MagicMock())

    with pytest.raises(AssertionError, match="requires operator-installation-config"):
        KubernetesTester.static_ga_config_for_current_operator(namespace="other-ns")


def test_operator_resolution_fails_when_the_operator_namespace_has_no_config(monkeypatch):
    monkeypatch.setenv("MDB_DEFAULT_ARCHITECTURE", "static-ga")
    monkeypatch.setenv("NAMESPACE", "operator-ns")
    core = MagicMock()
    core.read_namespaced_config_map.side_effect = kubernetes.client.exceptions.ApiException(status=404)
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    with pytest.raises(AssertionError, match="requires operator-installation-config"):
        KubernetesTester.static_ga_config_for_current_operator()


def test_operator_resolution_raises_other_api_errors(monkeypatch):
    monkeypatch.setenv("MDB_DEFAULT_ARCHITECTURE", "static-ga")
    monkeypatch.setenv("NAMESPACE", "operator-ns")
    core = MagicMock()
    core.read_namespaced_config_map.side_effect = kubernetes.client.exceptions.ApiException(status=403)
    monkeypatch.setattr(kubernetes.client, "CoreV1Api", lambda api_client=None: core)
    with pytest.raises(kubernetes.client.exceptions.ApiException):
        KubernetesTester.static_ga_config_for_current_operator()


# --- automation process names --------------------------------------------------------------------------------


@pytest.mark.parametrize(
    "namespace,pod_name,expected",
    [
        ("test-namespace", "my-replica-set-0", "k8s/test-namespace/my-replica-set-0"),
        ("a-1793870204-44z73bqtabz", "rs-sbbwj-2", "k8s/a-1793870204-44z73bqtabz/rs-sbbwj-2"),
    ],
)
def test_automation_process_name(namespace, pod_name, expected):
    assert automation_process_name(namespace, pod_name) == expected


# --- operator custom environment variables -------------------------------------------------------------------


@pytest.mark.parametrize(
    "initial,expected",
    [
        ("", "OPS_MANAGER_MONITOR_APPDB=true"),
        (
            r"MDB_TEST_ONLY_RELEASED_AGENT_PHASE=true",
            r"MDB_TEST_ONLY_RELEASED_AGENT_PHASE=true\&OPS_MANAGER_MONITOR_APPDB=true",
        ),
        (r"OTHER=a\&OPS_MANAGER_MONITOR_APPDB=false", r"OTHER=a\&OPS_MANAGER_MONITOR_APPDB=true"),
        (r"OPS_MANAGER_MONITOR_APPDB=true", "OPS_MANAGER_MONITOR_APPDB=true"),
    ],
)
def test_with_custom_env_var_merges_without_duplicates(initial, expected):
    config = {"customEnvVars": initial} if initial else {}
    assert with_custom_env_var(config, "OPS_MANAGER_MONITOR_APPDB", "true")["customEnvVars"] == expected


def test_released_agent_config_keeps_an_existing_marker_once():
    config = released_agent_config({"customEnvVars": r"MDB_TEST_ONLY_RELEASED_AGENT_PHASE=true"})
    assert config["customEnvVars"] == r"MDB_TEST_ONLY_RELEASED_AGENT_PHASE=true"


# --- the generic contract carries no topology or proxy acceptance -------------------------------------------


def test_generic_pod_helper_does_not_assert_proxy_internals(monkeypatch):
    """Proxy acceptance belongs to tests/replicaset/replica_set_mongodb_proxy.py, not to a shared helper.

    A functional test that asserts a running phase must not fail because of a candidate Agent image, a proxy
    image volume, the proxy API or the process separation.
    """
    sts = statefulset("mdb-rs", 1)
    monkeypatch.setattr("kubetester.kubetester.statefulset_pods", lambda *a, **k: [pod("mdb-rs-0", True)])

    calls = {}
    for name in ("query_proxy", "read_processes", "assert_agent_executable_sha256"):
        probe = MagicMock()
        monkeypatch.setattr(KubernetesTester, name, staticmethod(probe))
        calls[name] = probe
    architecture = MagicMock()
    monkeypatch.setattr("kubetester.kubetester.assert_statefulset_architecture", architecture)
    validate = MagicMock()
    monkeypatch.setattr("kubetester.kubetester.validate_static_ga_config", validate)

    KubernetesTester.assert_static_ga_pods(sts, NAMESPACE, database_running=True)

    for name, probe in calls.items():
        probe.assert_not_called()
    architecture.assert_not_called()
    validate.assert_not_called()


def test_phase_wait_does_not_enumerate_statefulsets(monkeypatch):
    """assert_reaches_phase is a phase wait. It performs no architecture, topology or proxy acceptance.

    origin/master has no hook there. Reintroducing one makes functional tests fail on proxy and topology
    conditions they do not own. The resource below exposes only ``wait_for``: any other attribute use fails.
    """
    listing = MagicMock(side_effect=AssertionError("the phase wait must not enumerate StatefulSets"))
    apps = MagicMock()
    apps.list_namespaced_stateful_set = listing
    monkeypatch.setattr(kubernetes.client, "AppsV1Api", lambda api_client=None: apps)

    class PhaseOnlyResource:
        """A resource that only supports the phase wait."""

        def __init__(self):
            self.waits = 0

        def wait_for(self, *args, **kwargs):
            self.waits += 1
            return True

        def __getattr__(self, name):
            raise AssertionError(f"the phase wait must not use {name}")

    resource = PhaseOnlyResource()
    MongoDB.assert_reaches_phase(cast(MongoDB, resource), Phase.Running)

    assert resource.waits == 1
    listing.assert_not_called()
