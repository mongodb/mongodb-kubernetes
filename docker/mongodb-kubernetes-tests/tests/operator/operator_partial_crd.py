# Dev note: remove all the CRDs before running the test locally!
from typing import Dict

import pytest
from kubernetes import client
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.operator import Operator, delete_operator_crds, list_operator_crds
from pytest import fixture


@fixture(scope="module")
def ops_manager_and_mongodb_crds():
    """Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_mongodb.yaml")
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_opsmanagers.yaml")


@fixture(scope="module")
def operator_only_ops_manager_and_mongodb(
    ops_manager_and_mongodb_crds,
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = operator_installation_config.copy()
    helm_args["operator.watchedResources"] = "{opsmanagers,mongodb}"

    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_options=["--skip-crds"],
    ).install()


@fixture(scope="module")
def mongodb_crds():
    """Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(client.api_client.ApiClient(), "helm_chart/crds/mongodb.com_mongodb.yaml")


@fixture(scope="module")
def operator_only_mongodb(
    mongodb_crds,
    namespace: str,
    operator_installation_config: Dict[str, str],
) -> Operator:
    helm_args = operator_installation_config.copy()
    helm_args["operator.watchedResources"] = "{mongodb}"

    return Operator(
        namespace=namespace,
        helm_args=helm_args,
        helm_options=["--skip-crds"],
    ).install()


@pytest.mark.e2e_operator_partial_crd
def test_install_operator_ops_manager_and_mongodb_only(
    operator_only_ops_manager_and_mongodb: Operator,
):
    """Note, that currently it's not possible to install OpsManager only as it requires MongoDB resources
    (it watches them internally)"""
    operator_only_ops_manager_and_mongodb.assert_is_running()


@pytest.mark.e2e_operator_partial_crd
def test_only_ops_manager_and_mongodb_crds_exist():
    operator_crds = list_operator_crds()
    assert len(operator_crds) == 2
    assert operator_crds[0].metadata.name == "mongodb.mongodb.com"
    assert operator_crds[1].metadata.name == "opsmanagers.mongodb.com"


@pytest.mark.e2e_operator_partial_crd
def test_remove_operator_and_crds(operator_only_ops_manager_and_mongodb: Operator):
    delete_operator_crds()
    operator_only_ops_manager_and_mongodb.uninstall()


@pytest.mark.e2e_operator_partial_crd
def test_install_operator_mongodb_only(operator_only_mongodb: Operator):
    operator_only_mongodb.assert_is_running()


@pytest.mark.e2e_operator_partial_crd
def test_only_mongodb_and_users_crds_exists():
    operator_crds = list_operator_crds()
    assert len(operator_crds) == 1
    assert operator_crds[0].metadata.name == "mongodb.mongodb.com"
