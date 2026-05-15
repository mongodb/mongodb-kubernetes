from typing import List

import kubernetes
import pytest
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture

from .conftest import cluster_spec_list

MDB_RESOURCE_NAME = "multi-replica-set-headless-mode"
AC_SECRET_KEY = "cluster-config.json"


def _ac_secret_name(mdb: MongoDBMulti) -> str:
    return f"{mdb.name}-config"


def _has_headless_agent_env(mdb: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]) -> bool:
    statefulsets = mdb.read_statefulsets(member_cluster_clients)
    for sts in statefulsets.values():
        for container in sts.spec.template.spec.containers:
            for env in container.env or []:
                if env.name == "HEADLESS_AGENT" and env.value == "true":
                    return True
    return False


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-headless.yaml"), MDB_RESOURCE_NAME, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@pytest.mark.e2e_multi_cluster_headless_mode
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_headless_mode
def test_create_headless(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_headless_agent_env_is_set(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    assert _has_headless_agent_env(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_automation_config_secret_exists(mongodb_multi: MongoDBMulti, namespace: str):
    secret = KubernetesTester.read_secret(namespace, _ac_secret_name(mongodb_multi))
    assert AC_SECRET_KEY in secret


@pytest.mark.e2e_multi_cluster_headless_mode
def test_migrate_headless_to_ops_manager(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["mode"] = "OpsManager"
    mongodb_multi["spec"]["credentials"] = "my-credentials"
    mongodb_multi["spec"]["opsManager"] = {"configMapRef": {"name": "my-project"}}
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=120)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_headless_agent_env_removed_after_migration(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    assert not _has_headless_agent_env(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_migrate_ops_manager_back_to_headless(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi["spec"]["mode"] = "Headless"
    mongodb_multi["spec"]["credentials"] = None
    mongodb_multi["spec"]["opsManager"] = None
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=120)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_headless_agent_env_restored_after_migration_back(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    assert _has_headless_agent_env(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_multi_cluster_headless_mode
def test_automation_config_secret_persists_after_round_trip(mongodb_multi: MongoDBMulti, namespace: str):
    secret = KubernetesTester.read_secret(namespace, _ac_secret_name(mongodb_multi))
    assert AC_SECRET_KEY in secret
