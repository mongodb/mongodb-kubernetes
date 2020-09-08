import pytest
from kubernetes import client
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from pytest import fixture

from kubetester.operator import Operator, delete_operator_crds, list_operator_crds

# Dev note: remove all the CRDs before running the test locally!


@fixture(scope="module")
def ops_manager_and_mongodb_crds():
    """ Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(
        client.api_client.ApiClient(), "helm_chart/crds/mongodb.mongodb.com.yaml"
    )
    create_or_replace_from_yaml(
        client.api_client.ApiClient(), "helm_chart/crds/opsmanagers.mongodb.com.yaml"
    )
    create_or_replace_from_yaml(
        client.api_client.ApiClient(), "helm_chart/crds/webhook-cluster-role.yaml"
    )


@fixture(scope="module")
def operator_only_ops_manager_and_mongodb(
    ops_manager_and_mongodb_crds,
    namespace: str,
    operator_version: str,
    operator_registry_url: str,
    om_init_registry_url: str,
    appdb_init_registry_url: str,
    database_init_registry_url: str,
    om_registry_url: str,
    appdb_registry_url: str,
    database_registry_url: str,
    ops_manager_name: str,
    appdb_name: str,
    database_name: str,
    managed_security_context: bool,
    image_pull_secrets: str,
) -> Operator:
    return Operator(
        namespace=namespace,
        operator_version=operator_version,
        operator_registry_url=operator_registry_url,
        init_om_registry_url=om_init_registry_url,
        init_appdb_registry_url=appdb_init_registry_url,
        init_database_registry_url=database_init_registry_url,
        ops_manager_registry_url=om_registry_url,
        appdb_registry_url=appdb_registry_url,
        database_registry_url=database_registry_url,
        ops_manager_name=ops_manager_name,
        appdb_name=appdb_name,
        database_name=database_name,
        managed_security_context=managed_security_context,
        image_pull_secrets=image_pull_secrets,
        helm_args={"operator.watchedResources": "{opsmanagers,mongodb}"},
        helm_options=["--skip-crds"],
    ).install()


@fixture(scope="module")
def mongodb_crds():
    """ Installs OM and MDB CRDs only (we need to do this manually as Helm 3 doesn't support templating for CRDs"""
    create_or_replace_from_yaml(
        client.api_client.ApiClient(), "helm_chart/crds/mongodb.mongodb.com.yaml"
    )


@fixture(scope="module")
def operator_only_mongodb(
    mongodb_crds,
    namespace: str,
    operator_version: str,
    operator_registry_url: str,
    om_init_registry_url: str,
    appdb_init_registry_url: str,
    database_init_registry_url: str,
    om_registry_url: str,
    appdb_registry_url: str,
    database_registry_url: str,
    ops_manager_name: str,
    appdb_name: str,
    database_name: str,
    managed_security_context: bool,
    image_pull_secrets: str,
) -> Operator:
    return Operator(
        namespace=namespace,
        operator_version=operator_version,
        operator_registry_url=operator_registry_url,
        init_om_registry_url=om_init_registry_url,
        init_appdb_registry_url=appdb_init_registry_url,
        init_database_registry_url=database_init_registry_url,
        ops_manager_registry_url=om_registry_url,
        appdb_registry_url=appdb_registry_url,
        database_registry_url=database_registry_url,
        ops_manager_name=ops_manager_name,
        appdb_name=appdb_name,
        database_name=database_name,
        managed_security_context=managed_security_context,
        image_pull_secrets=image_pull_secrets,
        helm_args={"operator.watchedResources": "{mongodb}"},
        helm_options=["--skip-crds"],
    ).install()


@pytest.mark.e2e_operator_partial_crd
def test_install_operator_ops_manager_and_mongodb_only(
    operator_only_ops_manager_and_mongodb: Operator,
):
    """ Note, that currently it's not possible to install OpsManager only as it requires MongoDB resources
    (it watches them internally) """
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
