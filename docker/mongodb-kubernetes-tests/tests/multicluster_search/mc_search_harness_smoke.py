"""MC E2E harness smoke test.

Exercises the harness primitives end-to-end against a real 2-cluster
kind setup:
  1. Create a fake Secret in the central cluster.
  2. Replicate it to each member cluster.
  3. Assert presence in each member cluster.
  4. Tear down.

This test does NOT exercise any MongoDBSearch / MongoDBMulti operator
code — it's solely a harness smoke. If this fails, MC e2e tests
that depend on the harness will also fail in non-obvious ways.
"""

from kubernetes.client import CoreV1Api, V1ObjectMeta, V1Secret
from pytest import fixture, mark
from tests import test_logger
from tests.common.multicluster_search.per_cluster_assertions import (
    assert_resource_in_cluster,
)
from tests.common.multicluster_search.secret_replicator import replicate_secret

logger = test_logger.get_test_logger(__name__)

SECRET_NAME = "mc-harness-smoke-fake-secret"


@fixture(scope="module")
def central_core(central_cluster_client) -> CoreV1Api:
    return CoreV1Api(api_client=central_cluster_client)


@fixture(scope="module")
def member_cores(member_cluster_clients) -> dict[str, CoreV1Api]:
    return {mcc.cluster_name: CoreV1Api(api_client=mcc.api_client) for mcc in member_cluster_clients}


@mark.e2e_mc_search_harness_smoke
def test_create_fake_secret_in_central(central_core: CoreV1Api, namespace: str):
    body = V1Secret(
        metadata=V1ObjectMeta(name=SECRET_NAME, namespace=namespace),
        type="Opaque",
        data={"smoke.txt": b"aGVsbG8="},  # base64("hello")
    )
    central_core.create_namespaced_secret(namespace=namespace, body=body)
    logger.info(f"created fake Secret {SECRET_NAME} in central cluster, ns={namespace}")


@mark.e2e_mc_search_harness_smoke
def test_replicate_secret_to_members(
    central_core: CoreV1Api,
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    replicate_secret(
        secret_name=SECRET_NAME,
        namespace=namespace,
        central_client=central_core,
        member_clients=member_cores,
    )


@mark.e2e_mc_search_harness_smoke
def test_assert_secret_present_in_each_member(
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    for cluster_name, core in member_cores.items():
        assert_resource_in_cluster(core, kind="Secret", name=SECRET_NAME, namespace=namespace)
        logger.info(f"verified {SECRET_NAME} present in cluster {cluster_name}")


@mark.e2e_mc_search_harness_smoke
def test_replicate_idempotent_on_second_call(
    central_core: CoreV1Api,
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    replicate_secret(
        secret_name=SECRET_NAME,
        namespace=namespace,
        central_client=central_core,
        member_clients=member_cores,
    )


@mark.e2e_mc_search_harness_smoke
def test_cleanup(central_core: CoreV1Api, member_cores: dict[str, CoreV1Api], namespace: str):
    central_core.delete_namespaced_secret(name=SECRET_NAME, namespace=namespace)
    for core in member_cores.values():
        try:
            core.delete_namespaced_secret(name=SECRET_NAME, namespace=namespace)
        except Exception as e:
            logger.debug(f"cleanup: {e}")
