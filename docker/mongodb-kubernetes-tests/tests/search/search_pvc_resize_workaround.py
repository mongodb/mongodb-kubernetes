import time

import pymongo.errors
from kubernetes import client
from kubernetes.utils import parse_quantity
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.connectivity import mongot_data_pvc_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"

STORAGE_CLASS = "csi-hostpath-sc"
INITIAL_STORAGE = "1Gi"
RESIZED_STORAGE = "2Gi"

MONGOT_CONTAINER = "mongot"
MONGOT_DATA_PATH = "/mongot/data"
MARKER_FILE = f"{MONGOT_DATA_PATH}/pvc-resize-marker"

EVIDENCE = "PVC-RESIZE-EVIDENCE:"

SEARCH_NOT_ENABLED = 31082

# How long kubelet gets to finish an online filesystem expansion before the pod is restarted.
FS_RESIZE_GRACE_SECONDS = 180

# Observations carried across the ordered steps of the workaround.
baseline: dict = {}


def evidence(msg: str):
    logger.info(f"{EVIDENCE} {msg}")


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource["spec"]["clusters"][0]["persistence"] = {
        "single": {"storage": INITIAL_STORAGE, "storageClass": STORAGE_CLASS}
    }
    return resource


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, namespace: str) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(mdbc, USER_NAME, USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


def sts_name(mdbs: MongoDBSearch) -> str:
    return search_resource_names.mongot_statefulset_name(mdbs.name)


def read_sts(namespace: str, name: str) -> client.V1StatefulSet:
    return client.AppsV1Api().read_namespaced_stateful_set(name, namespace)


def vct_storage(sts: client.V1StatefulSet) -> str:
    return sts.spec.volume_claim_templates[0].spec.resources.requests["storage"]


def read_pvcs(namespace: str, name: str) -> list[client.V1PersistentVolumeClaim]:
    core = client.CoreV1Api()
    return [
        core.read_namespaced_persistent_volume_claim(pvc_name, namespace)
        for pvc_name in sorted(mongot_data_pvc_names(namespace, name))
    ]


def owner_refs(obj) -> list[str]:
    return [f"{o.kind}/{o.name}(uid={o.uid})" for o in (obj.metadata.owner_references or [])]


def log_pvcs(stage: str, pvcs: list[client.V1PersistentVolumeClaim]):
    for pvc in pvcs:
        conditions = [c.type for c in (pvc.status.conditions or [])]
        evidence(
            f"[{stage}] PVC {pvc.metadata.name} uid={pvc.metadata.uid} volumeName={pvc.spec.volume_name} "
            f"storageClass={pvc.spec.storage_class_name} "
            f"spec.resources.requests.storage={pvc.spec.resources.requests['storage']} "
            f"status.capacity.storage={(pvc.status.capacity or {}).get('storage')} "
            f"conditions={conditions} ownerRefs={owner_refs(pvc)}"
        )


def mongot_pods(namespace: str, name: str, replicas: int) -> list[client.V1Pod]:
    core = client.CoreV1Api()
    return [core.read_namespaced_pod(f"{name}-{ordinal}", namespace) for ordinal in range(replicas)]


def log_pods(stage: str, pods: list[client.V1Pod]):
    for pod in pods:
        restarts = {cs.name: cs.restart_count for cs in (pod.status.container_statuses or [])}
        evidence(
            f"[{stage}] pod {pod.metadata.name} uid={pod.metadata.uid} phase={pod.status.phase} "
            f"restartCounts={restarts} revision={pod.metadata.labels.get('controller-revision-hash')} "
            f"ownerRefs={owner_refs(pod)}"
        )


def mongot_exec(namespace: str, pod_name: str, cmd: list[str]) -> str:
    return KubernetesTester.run_command_in_pod_container(pod_name, namespace, cmd, container=MONGOT_CONTAINER)


def data_mount_size_bytes(stage: str, namespace: str, pod_name: str) -> int:
    out = mongot_exec(namespace, pod_name, ["df", "-kP", MONGOT_DATA_PATH])
    evidence(f"[{stage}] {pod_name} df -kP {MONGOT_DATA_PATH}:\n{out}")
    return int(out.strip().splitlines()[-1].split()[1]) * 1024


def search_hit_count(helper: SampleMoviesSearchHelper) -> int:
    return len(list(helper.execute_example_search_query()))


@mark.e2e_search_pvc_resize_workaround
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_pvc_resize_workaround
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_pvc_resize_workaround
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_pvc_resize_workaround
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_pvc_resize_workaround
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_pvc_resize_workaround
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_pvc_resize_workaround
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    def created() -> tuple[bool, str]:
        try:
            sample_movies_helper.create_search_index()
        except pymongo.errors.OperationFailure as exc:
            # mongod reports SearchNotEnabled until its rolling restart with mongotHost completes.
            if exc.code != SEARCH_NOT_ENABLED:
                raise
            return False, str(exc)
        return True, "created"

    run_periodically(created, timeout=600, sleep_time=10, msg="search index creation")


@mark.e2e_search_pvc_resize_workaround
def test_search_query_before_resize(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=300)
    hits = search_hit_count(sample_movies_helper)
    evidence(f"[before] $search hit count={hits}")
    assert hits > 0
    baseline["hits"] = hits


@mark.e2e_search_pvc_resize_workaround
def test_record_baseline(namespace: str, mdbs: MongoDBSearch):
    version = client.VersionApi().get_code()
    evidence(f"kubernetes server version={version.git_version}")

    sts = read_sts(namespace, sts_name(mdbs))
    evidence(
        f"[before] STS {sts.metadata.name} uid={sts.metadata.uid} volumeClaimTemplate.storage={vct_storage(sts)} "
        f"replicas={sts.spec.replicas} pvcRetentionPolicy={sts.spec.persistent_volume_claim_retention_policy}"
    )
    assert vct_storage(sts) == INITIAL_STORAGE

    pvcs = read_pvcs(namespace, sts.metadata.name)
    assert len(pvcs) == sts.spec.replicas
    log_pvcs("before", pvcs)

    pods = mongot_pods(namespace, sts.metadata.name, sts.spec.replicas)
    log_pods("before", pods)

    for pod in pods:
        mongot_exec(namespace, pod.metadata.name, ["/bin/sh", "-c", f"echo {pod.metadata.uid} > {MARKER_FILE}"])
        data_mount_size_bytes("before", namespace, pod.metadata.name)

    baseline.update(
        sts_uid=sts.metadata.uid,
        replicas=sts.spec.replicas,
        pvc_uids={pvc.metadata.name: pvc.metadata.uid for pvc in pvcs},
        pv_names={pvc.metadata.name: pvc.spec.volume_name for pvc in pvcs},
        pod_uids={pod.metadata.name: pod.metadata.uid for pod in pods},
    )


@mark.e2e_search_pvc_resize_workaround
def test_step1_storage_class_allows_expansion():
    sc = client.StorageV1Api().read_storage_class(STORAGE_CLASS)
    evidence(f"[step1] StorageClass {STORAGE_CLASS} allowVolumeExpansion={sc.allow_volume_expansion}")
    assert sc.allow_volume_expansion is True


@mark.e2e_search_pvc_resize_workaround
def test_step2_patch_pvcs(namespace: str, mdbs: MongoDBSearch):
    core = client.CoreV1Api()
    for pvc_name in baseline["pvc_uids"]:
        core.patch_namespaced_persistent_volume_claim(
            pvc_name, namespace, {"spec": {"resources": {"requests": {"storage": RESIZED_STORAGE}}}}
        )
        evidence(f"[step2] patched PVC {pvc_name} spec.resources.requests.storage={RESIZED_STORAGE}")
    log_pvcs("after-patch", read_pvcs(namespace, sts_name(mdbs)))


@mark.e2e_search_pvc_resize_workaround
def test_step3_wait_for_pvc_capacity(namespace: str, mdbs: MongoDBSearch):
    core = client.CoreV1Api()
    name = sts_name(mdbs)
    fs_resize_pending_since: dict[str, float] = {}
    restarted_for_fs_resize: set[str] = set()

    def expanded() -> tuple[bool, str]:
        pvcs = read_pvcs(namespace, name)
        pending = []
        for pvc in pvcs:
            capacity = (pvc.status.capacity or {}).get("storage", "0")
            if parse_quantity(capacity) < parse_quantity(RESIZED_STORAGE):
                pending.append(f"{pvc.metadata.name}={capacity}")
            conditions = {c.type for c in (pvc.status.conditions or [])}
            if "FileSystemResizePending" not in conditions or pvc.metadata.name in restarted_for_fs_resize:
                continue
            if pvc.metadata.name not in fs_resize_pending_since:
                fs_resize_pending_since[pvc.metadata.name] = time.time()
                evidence(f"[step3] {pvc.metadata.name} has FileSystemResizePending, waiting for online expansion")
            elif time.time() - fs_resize_pending_since[pvc.metadata.name] > FS_RESIZE_GRACE_SECONDS:
                pod_name = pvc.metadata.name.removeprefix("data-")
                evidence(
                    f"[step3] {pvc.metadata.name} still FileSystemResizePending after {FS_RESIZE_GRACE_SECONDS}s, "
                    f"deleting pod {pod_name}"
                )
                core.delete_namespaced_pod(pod_name, namespace)
                restarted_for_fs_resize.add(pvc.metadata.name)
        return not pending, f"pending={pending}"

    started = time.time()
    run_periodically(expanded, timeout=900, sleep_time=5, msg="mongot data PVCs to report expanded capacity")
    evidence(f"[step3] PVC capacity reached {RESIZED_STORAGE} after {time.time() - started:.0f}s")

    pvcs = read_pvcs(namespace, name)
    log_pvcs("after-expand", pvcs)
    evidence(f"[step3] pods deleted for FileSystemResizePending: {sorted(restarted_for_fs_resize) or 'none'}")
    for pvc in pvcs:
        assert parse_quantity(pvc.status.capacity["storage"]) >= parse_quantity(RESIZED_STORAGE)
        assert pvc.metadata.uid == baseline["pvc_uids"][pvc.metadata.name]

    if restarted_for_fs_resize:
        run_periodically(
            lambda: (read_sts(namespace, name).status.ready_replicas or 0) == baseline["replicas"],
            timeout=600,
            sleep_time=5,
            msg="mongot pods Ready after FileSystemResizePending restart",
        )


@mark.e2e_search_pvc_resize_workaround
def test_step4_update_search_storage(
    namespace: str, mdbs: MongoDBSearch, sample_movies_helper: SampleMoviesSearchHelper
):
    mdbs["spec"]["clusters"][0]["persistence"]["single"]["storage"] = RESIZED_STORAGE
    mdbs.update()

    mdbs.assert_reaches_phase(Phase.Failed, msg_regexp="(?s).*Forbidden", timeout=300)
    mdbs.load()
    evidence(f"[step4] MongoDBSearch phase={mdbs.get_status_phase()} message={mdbs.get_status_message()!r}")

    sts = read_sts(namespace, sts_name(mdbs))
    evidence(f"[step4] STS {sts.metadata.name} uid={sts.metadata.uid} volumeClaimTemplate.storage={vct_storage(sts)}")
    assert sts.metadata.uid == baseline["sts_uid"]
    assert vct_storage(sts) == INITIAL_STORAGE

    hits = search_hit_count(sample_movies_helper)
    evidence(f"[step4] $search hit count while CR Failed={hits}")
    assert hits == baseline["hits"]


@mark.e2e_search_pvc_resize_workaround
def test_step5_orphan_delete_statefulset(namespace: str, mdbs: MongoDBSearch):
    name = sts_name(mdbs)
    pods = mongot_pods(namespace, name, baseline["replicas"])
    log_pods("before-sts-delete", pods)
    baseline["pod_uids_before_sts_delete"] = {pod.metadata.name: pod.metadata.uid for pod in pods}

    client.AppsV1Api().delete_namespaced_stateful_set(name, namespace, propagation_policy="Orphan")
    baseline["deleted_at"] = time.time()
    evidence(f"[step5] deleted STS {name} uid={baseline['sts_uid']} with propagationPolicy=Orphan")


@mark.e2e_search_pvc_resize_workaround
def test_step6_statefulset_recreated_with_new_size(namespace: str, mdbs: MongoDBSearch):
    name = sts_name(mdbs)

    def recreated() -> tuple[bool, str]:
        try:
            sts = read_sts(namespace, name)
        except client.exceptions.ApiException as exc:
            if exc.status == 404:
                return False, "STS absent"
            raise
        return (
            sts.metadata.uid != baseline["sts_uid"] and vct_storage(sts) == RESIZED_STORAGE,
            f"uid={sts.metadata.uid} storage={vct_storage(sts)}",
        )

    run_periodically(recreated, timeout=600, sleep_time=2, msg=f"operator to recreate STS {name} with new size")
    sts = read_sts(namespace, name)
    evidence(
        f"[step6] STS recreated after {time.time() - baseline['deleted_at']:.0f}s: old uid={baseline['sts_uid']} "
        f"new uid={sts.metadata.uid} volumeClaimTemplate.storage={vct_storage(sts)} "
        f"pvcRetentionPolicy={sts.spec.persistent_volume_claim_retention_policy}"
    )
    baseline["new_sts_uid"] = sts.metadata.uid


@mark.e2e_search_pvc_resize_workaround
def test_step6_pvcs_kept_not_replaced(namespace: str, mdbs: MongoDBSearch):
    pvcs = read_pvcs(namespace, sts_name(mdbs))
    log_pvcs("after-recreate", pvcs)
    assert {pvc.metadata.name: pvc.metadata.uid for pvc in pvcs} == baseline["pvc_uids"]
    assert {pvc.metadata.name: pvc.spec.volume_name for pvc in pvcs} == baseline["pv_names"]
    for pvc in pvcs:
        assert pvc.metadata.deletion_timestamp is None
        assert parse_quantity(pvc.status.capacity["storage"]) >= parse_quantity(RESIZED_STORAGE)


@mark.e2e_search_pvc_resize_workaround
def test_step6_search_resource_running(mdbs: MongoDBSearch):
    mdbs.assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)
    mdbs.load()
    evidence(
        f"[step6] MongoDBSearch phase={mdbs.get_status_phase()} message={mdbs.get_status_message()!r} "
        f"{time.time() - baseline['deleted_at']:.0f}s after STS delete"
    )


@mark.e2e_search_pvc_resize_workaround
def test_step6_mongot_pods_ready(namespace: str, mdbs: MongoDBSearch):
    name = sts_name(mdbs)
    run_periodically(
        lambda: (read_sts(namespace, name).status.ready_replicas or 0) == baseline["replicas"],
        timeout=600,
        sleep_time=5,
        msg=f"mongot STS {name} pods Ready",
    )
    pods = mongot_pods(namespace, name, baseline["replicas"])
    log_pods("after", pods)
    for pod in pods:
        before = baseline["pod_uids_before_sts_delete"][pod.metadata.name]
        evidence(
            f"[step6] pod {pod.metadata.name} uid before STS delete={before} after={pod.metadata.uid} "
            f"replaced by STS recreate={pod.metadata.uid != before}"
        )


@mark.e2e_search_pvc_resize_workaround
def test_step6_pvc_owner_refs_readopted(namespace: str, mdbs: MongoDBSearch):
    name = sts_name(mdbs)

    def readopted() -> tuple[bool, str]:
        pvcs = read_pvcs(namespace, name)
        refs = {pvc.metadata.name: owner_refs(pvc) for pvc in pvcs}
        uids = {pvc.metadata.name: [o.uid for o in (pvc.metadata.owner_references or [])] for pvc in pvcs}
        return all(baseline["new_sts_uid"] in u for u in uids.values()), f"ownerRefs={refs}"

    run_periodically(readopted, timeout=300, sleep_time=5, msg="data PVCs owned by the recreated STS")
    log_pvcs("readopted", read_pvcs(namespace, name))


@mark.e2e_search_pvc_resize_workaround
def test_step6_same_volume_mounted_in_mongot(namespace: str, mdbs: MongoDBSearch):
    for pod in mongot_pods(namespace, sts_name(mdbs), baseline["replicas"]):
        marker = mongot_exec(namespace, pod.metadata.name, ["cat", MARKER_FILE]).strip()
        evidence(f"[step6] {pod.metadata.name} marker file {MARKER_FILE} content={marker!r}")
        assert marker in baseline["pod_uids"].values()
        assert data_mount_size_bytes("after", namespace, pod.metadata.name) >= parse_quantity(RESIZED_STORAGE)


@mark.e2e_search_pvc_resize_workaround
def test_step6_search_query_without_reingest(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=300)
    hits = search_hit_count(sample_movies_helper)
    evidence(f"[after] $search hit count={hits} (before={baseline['hits']})")
    assert hits == baseline["hits"]
