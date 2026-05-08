"""Reusable bootstrap stages for RS managed-LB MongoDBSearch e2e tests.

The 14-stage RS-+-Search-+-managed-LB-+-TLS bootstrap (install operator,
optional OM, deploy CA / TLS / users, create the MongoDB and MongoDBSearch
resources, verify envoy/mongod parameters, restore sample data, build the
search index) was duplicated almost verbatim across the connectivity-tool
e2e and the existing managed-LB e2es. This module exposes each stage as a
small free-standing function so test files can stay near-empty pytest
shells that delegate marker-only.

Sibling tests (``search_replicaset_internal_mongodb_multi_mongot_managed_lb``
and friends) can migrate to call into these helpers in follow-up PRs; the
KUBE-17 connectivity-tool e2e is the first consumer.
"""

from __future__ import annotations

from kubernetes import client
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.phase import Phase
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    get_rs_search_tester,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)


def install_operator(namespace: str, operator_installation_config: dict[str, str]) -> None:
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


def create_ops_manager(namespace: str) -> None:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


def install_tls_certificates(helper: SearchDeploymentHelper, issuer: str, members: int) -> None:
    helper.install_rs_tls_certificates(issuer, members=members)


def create_database_resource(mdb: MongoDB) -> None:
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


def create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    admin_password: str,
    user: MongoDBUser,
    user_password: str,
    mongot_user: MongoDBUser,
    mongot_password: str,
) -> None:
    helper.deploy_users(
        admin_user,
        admin_password,
        user,
        user_password,
        mongot_user,
        mongot_password,
    )


def deploy_lb_certificates(namespace: str, issuer: str, mdbs_resource_name: str, prefix: str) -> None:
    create_rs_lb_certificates(namespace, issuer, mdbs_resource_name, prefix)


def create_search_tls_certificate(namespace: str, issuer: str, mdbs_resource_name: str, prefix: str) -> None:
    create_rs_search_tls_cert(namespace, issuer, mdbs_resource_name, prefix)


def create_search_resource(mdbs: MongoDBSearch) -> None:
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


def verify_envoy_deployment(namespace: str, mdbs_resource_name: str) -> None:
    envoy_deployment_name = search_resource_names.lb_deployment_name(mdbs_resource_name)

    def check_envoy_deployment():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_deployment_name} not found: {e}"

    run_periodically(check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}")


def wait_for_database_ready(mdb: MongoDB) -> None:
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


def verify_mongod_parameters(
    namespace: str,
    mdb_resource_name: str,
    members: int,
    mdbs_name: str,
    envoy_proxy_port: int,
) -> None:
    expected_host = search_resource_names.proxy_service_host(mdbs_name, namespace, envoy_proxy_port)
    verify_rs_mongod_parameters(namespace, mdb_resource_name, members, expected_host)


def restore_sample_database(
    mdb: MongoDB,
    tools_pod: mongodb_tools_pod.ToolsPod,
    admin_user_name: str,
    admin_password: str,
) -> None:
    search_tester = get_rs_search_tester(mdb, admin_user_name, admin_password, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


def create_search_index(mdb: MongoDB, user_name: str, user_password: str) -> None:
    search_tester = get_rs_search_tester(mdb, user_name, user_password, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
