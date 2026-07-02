# Dev note: remove all the CRDs before running the test locally!
from typing import Dict

import pytest
from kubernetes import client
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.helm import apply_operator_config_crd
from kubetester.kubetester import create_operator_config
from kubetester.operator import Operator, delete_operator_crds, list_operator_crds
from pytest import fixture


@fixture(scope="module")
def ops_manager_and_mongodb_crds():
    """Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_mongodb.yaml")
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_opsmanagers.yaml")
    # apply_operator_config_crd waits for the CRD to be Established before we create the CR below;
    # create_or_replace_from_yaml does not, which races the API server after a prior CRD deletion.
    apply_operator_config_crd(api_client=client.api_client.ApiClient())


@fixture(scope="module")
def operator_only_ops_manager_and_mongodb(
    ops_manager_and_mongodb_crds,
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    # Restrict which CRDs the operator reconciles via OperatorConfig. The CR must exist before the
    # operator starts: with no CR it defaults to watching all CRDs and would crash on the ones that
    # aren't installed in this test.
    create_operator_config(namespace, {"watchedResources": ["opsmanagers", "mongodb"]})

    return Operator(
        namespace=namespace,
        helm_args=operator_installation_config,
        helm_options=["--skip-crds"],
    ).install()


@fixture(scope="module")
def mongodb_crds():
    """Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_mongodb.yaml")
    # Waits for the CRD to be Established before we create the CR below. This fixture runs after
    # test_remove_operator_and_crds deletes the CRD, so the API server must re-register the endpoint.
    apply_operator_config_crd(api_client=client.api_client.ApiClient())


@fixture(scope="module")
def operator_only_mongodb(
    mongodb_crds,
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    # See operator_only_ops_manager_and_mongodb: the OperatorConfig CR must exist before the operator
    # starts so it only reconciles the installed CRDs.
    create_operator_config(namespace, {"watchedResources": ["mongodb"]})

    return Operator(
        namespace=namespace,
        helm_args=operator_installation_config,
        helm_options=["--skip-crds"],
    ).install()


@pytest.mark.e2e_operator_partial_crd
def test_install_operator_ops_manager_and_mongodb_only(
    operator_only_ops_manager_and_mongodb: Operator,
):
    """Note, that currently it's not possible to install OpsManager only as it requires MongoDB resources
    (it watches them internally)"""
    operator_only_ops_manager_and_mongodb.wait_for_operator_ready()


@pytest.mark.e2e_operator_partial_crd
def test_only_ops_manager_and_mongodb_crds_exist():
    operator_crds = list_operator_crds()
    assert len(operator_crds) == 3
    assert operator_crds[0].metadata.name == "mongodb.mongodb.com"
    assert operator_crds[1].metadata.name == "operatorconfigs.operator.mongodb.com"
    assert operator_crds[2].metadata.name == "opsmanagers.mongodb.com"


@pytest.mark.e2e_operator_partial_crd
def test_remove_operator_and_crds(operator_only_ops_manager_and_mongodb: Operator):
    delete_operator_crds()
    operator_only_ops_manager_and_mongodb.uninstall()


@pytest.mark.e2e_operator_partial_crd
def test_install_operator_mongodb_only(operator_only_mongodb: Operator):
    operator_only_mongodb.wait_for_operator_ready()


@pytest.mark.e2e_operator_partial_crd
def test_only_mongodb_and_users_crds_exists():
    operator_crds = list_operator_crds()
    assert len(operator_crds) == 2
    assert operator_crds[0].metadata.name == "mongodb.mongodb.com"
    assert operator_crds[1].metadata.name == "operatorconfigs.operator.mongodb.com"
