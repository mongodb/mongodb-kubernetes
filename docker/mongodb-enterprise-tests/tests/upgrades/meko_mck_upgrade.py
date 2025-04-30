from typing import Dict, List

import kubernetes
from kubernetes import client
from kubetester import MongoDB, try_load
from kubetester.certs import create_mongodb_tls_certs
from kubetester.helm import helm_uninstall
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger
from tests.common.constants import MONGODB_PORT
from tests.conftest import (
    LEGACY_MULTI_CLUSTER_OPERATOR_NAME,
    LEGACY_OPERATOR_NAME,
    MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP,
    MULTI_CLUSTER_OPERATOR_NAME,
    OPERATOR_NAME,
    get_multi_cluster_operator,
    is_multi_cluster,
    log_deployments_info,
    setup_log_rotate_for_agents,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.multicluster_appdb_state_operator_upgrade_downgrade import (
    assert_cm_expected_data,
)
from tests.upgrades import downscale_operator_deployment

logger = test_logger.get_test_logger(__name__)

RS_NAME = "my-replica-set"
CERT_PREFIX = "prefix"


@fixture(scope="module")
def rs_certs_secret(namespace: str, issuer: str):
    return create_mongodb_tls_certs(issuer, namespace, RS_NAME, "{}-{}-cert".format(CERT_PREFIX, RS_NAME))


@fixture(scope="module")
def replica_set(
    namespace: str,
    issuer_ca_configmap: str,
    rs_certs_secret: str,
    custom_mdb_version: str,
    member_cluster_names: List[str],
    central_cluster_client: client.ApiClient,
) -> MongoDB:
    if is_multi_cluster():
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("mongodb-multi-cluster.yaml"),
            "multi-replica-set",
            namespace,
        )
        resource.set_version(custom_mdb_version)
        resource["spec"]["persistent"] = False
        resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])

        if try_load(resource):
            return resource

        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
        resource.set_architecture_annotation()

        resource.update()
        return resource
    else:
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set.yaml"),
            namespace=namespace,
            name=RS_NAME,
        )
        resource.set_version(custom_mdb_version)

        # Make sure we persist in order to be able to upgrade gracefully
        # and it is also faster.
        resource["spec"]["persistent"] = True

        # TLS
        resource.configure_custom_tls(
            issuer_ca_configmap,
            CERT_PREFIX,
        )

        # SCRAM-SHA
        resource["spec"]["security"]["authentication"] = {
            "enabled": True,
            "modes": ["SCRAM"],
        }

        if try_load(resource):
            return resource
        return resource.create()


# Installs the latest officially released version of MEKO, from Quay
@mark.e2e_meko_mck_upgrade
def test_install_latest_official_operator(official_operator: Operator, namespace: str):
    official_operator.assert_is_running()
    # Dumping deployments in logs ensures we are using the correct operator version
    log_deployments_info(namespace)


@mark.e2e_meko_mck_upgrade
def test_install_replicaset(replica_set: MongoDB):
    replica_set.assert_reaches_phase(phase=Phase.Running, timeout=1000 if is_multi_cluster() else 600)


@mark.e2e_meko_mck_upgrade
def test_downscale_latest_official_operator(namespace: str):
    deployment_name = LEGACY_MULTI_CLUSTER_OPERATOR_NAME if is_multi_cluster() else LEGACY_OPERATOR_NAME
    downscale_operator_deployment(deployment_name, namespace)


# Upgrade to MCK
@mark.e2e_meko_mck_upgrade
def test_upgrade_operator(
    namespace: str,
    operator_installation_config,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: Dict[str, str],
    central_cluster_client: client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
):
    logger.info("Installing the operator via repo helm chart")
    if is_multi_cluster():
        operator = get_multi_cluster_operator(
            namespace,
            central_cluster_name,
            multi_cluster_operator_installation_config,
            central_cluster_client,
            member_cluster_clients,
            member_cluster_names,
        )
    else:
        operator = Operator(
            namespace=namespace,
            helm_args=operator_installation_config,
            helm_chart_path="helm_chart",
            name="mongodb-kubernetes-operator",
        )
        operator.install()
    operator.assert_is_running()
    log_deployments_info(namespace)


@mark.e2e_meko_mck_upgrade
def test_replicaset_reconciled(replica_set: MongoDB):
    replica_set.assert_abandons_phase(phase=Phase.Running, timeout=300)
    replica_set.assert_reaches_phase(phase=Phase.Running, timeout=800)


@mark.e2e_meko_mck_upgrade
def test_uninstall_latest_official_operator(namespace: str):
    helm_uninstall("mongodb-enterprise-operator-multi-cluster" if is_multi_cluster() else "mongodb-enterprise-operator")
    log_deployments_info(namespace)


@mark.e2e_meko_mck_upgrade
def test_operator_still_running(namespace: str, central_cluster_client: client.ApiClient, member_cluster_names):
    operator_name = MULTI_CLUSTER_OPERATOR_NAME if is_multi_cluster() else OPERATOR_NAME
    operator_instance = Operator(
        name=operator_name,
        namespace=namespace,
    )
    logger.info(f"Checking status of operator '{operator_name}' in namespace '{namespace}'")
    operator_instance.assert_is_running()
    log_deployments_info(namespace)

    if is_multi_cluster():
        # Check if member-list configmap is present and content is correct
        logger.info(f"Checking correctness of member list configmap")
        expected_data = {name: "" for name in member_cluster_names}
        assert_cm_expected_data(
            name=MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP,
            namespace=namespace,
            expected_data=expected_data,
            central_cluster_client=central_cluster_client,
        )
