from typing import Optional

import kubernetes.client
import pytest
from kubernetes.client.rest import ApiException
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    APPDB_NAME,
    appdb_ca_configmap as helper_appdb_ca_configmap,
    appdb_cert_prefix as helper_appdb_cert_prefix,
    appdb_member_cluster_names as default_appdb_member_cluster_names,
    assert_multi_cluster_appdb_statefulset_identity,
    assert_no_internal_appdb_statefulset,
    configure_appdb_role_mongodb_multi,
    multi_cluster_appdb_role_resource,
    primary_om_resource,
)


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return default_appdb_member_cluster_names()


@fixture(scope="module")
def meta_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta_om.yaml"), name="meta-om", namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(
    namespace: str, custom_version: str, central_cluster_client: kubernetes.client.ApiClient
) -> MongoDBOpsManager:
    resource = primary_om_resource(namespace, custom_version)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def appdb_ca_configmap(multi_cluster_issuer_ca_configmap: str) -> str:
    return helper_appdb_ca_configmap(multi_cluster_issuer_ca_configmap)


@fixture(scope="module")
def appdb_cert_prefix(namespace: str, multi_cluster_issuer: str) -> str:
    return helper_appdb_cert_prefix(namespace, multi_cluster_issuer)


@fixture(scope="module")
def external_appdb(
    namespace: str,
    custom_mdb_version: str,
    appdb_cert_prefix: str,
    appdb_ca_configmap: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = multi_cluster_appdb_role_resource(
        namespace,
        custom_mdb_version,
        appdb_cert_prefix,
        appdb_ca_configmap,
        appdb_member_cluster_names,
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_fresh
class TestDeployMetaOpsManager:
    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_fresh
class TestFreshStartExternalAppDB:
    def test_create_appdb_role_mongodb(
        self,
        external_appdb: MongoDBMulti,
        meta_om: MongoDBOpsManager,
        namespace: str,
        appdb_ca_configmap: str,
    ):
        configure_appdb_role_mongodb_multi(external_appdb, meta_om, namespace, appdb_ca_configmap)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_ref_and_no_internal_appdb(
        self,
        primary_om: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)

        with pytest.raises(ApiException, match=r"NotFound|not found"):
            assert_no_internal_appdb_statefulset(namespace, APPDB_NAME, central_cluster_client)

    def test_external_appdb_statefulsets_are_adopted(
        self,
        external_appdb: MongoDBMulti,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
    ):
        assert_multi_cluster_appdb_statefulset_identity(
            external_appdb,
            appdb_member_cluster_names,
        )
