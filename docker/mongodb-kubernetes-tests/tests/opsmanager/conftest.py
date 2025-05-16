#!/usr/bin/env python3

import os
from pathlib import Path
from typing import Dict, Optional

from kubernetes import client
from kubetester import get_pod_when_ready
from kubetester.helm import helm_install_from_chart
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.conftest import is_multi_cluster

MINIO_OPERATOR = "minio-operator"
MINIO_TENANT = "minio-tenant"


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test"""
    if is_multi_cluster():
        if item.fixturenames not in (
            "multi_cluster_operator_with_monitored_appdb",
            "multi_cluster_operator",
        ):
            print("\nAdding operator installation fixture: multi_cluster_operator")
            item.fixturenames.insert(0, "multi_cluster_operator_with_monitored_appdb")
    elif item.fixturenames not in [
        "default_operator",
        "operator_with_monitored_appdb",
        "multi_cluster_operator_with_monitored_appdb",
        "multi_cluster_operator",
    ]:
        item.fixturenames.insert(0, "default_operator")


@fixture(scope="module")
def custom_om_prev_version() -> str:
    """Returns a CUSTOM_OM_PREV_VERSION for OpsManager to be created/upgraded."""
    return os.getenv("CUSTOM_OM_PREV_VERSION", "6.0.0")


@fixture(scope="module")
def custom_mdb_prev_version() -> str:
    """Returns a CUSTOM_MDB_PREV_VERSION for Mongodb to be created/upgraded to for testing.
    Used for backup mainly (to test backup for different mdb versions).
    Defaults to 4.4.24 (simplifies testing locally)"""
    return os.getenv("CUSTOM_MDB_PREV_VERSION", "5.0.15")


@fixture(scope="module")
def gen_key_resource_version(ops_manager: MongoDBOpsManager) -> str:
    secret = ops_manager.read_gen_key_secret()
    return secret.metadata.resource_version


@fixture(scope="module")
def admin_key_resource_version(ops_manager: MongoDBOpsManager) -> str:
    secret = ops_manager.read_api_key_secret()
    return secret.metadata.resource_version


def mino_operator_install(
    namespace: str,
    operator_name: str = MINIO_OPERATOR,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    helm_args: Dict[str, str] = None,
    version="5.0.6",
):
    if cluster_name is not None:
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    if helm_args is None:
        helm_args = {}
    helm_args.update(
        {
            "namespace": namespace,
            "fullnameOverride": operator_name,
            "nameOverride": operator_name,
        }
    )

    # check if the pod exists, if not do a helm upgrade
    operator_pod = client.CoreV1Api(api_client=cluster_client).list_namespaced_pod(
        namespace, label_selector=f"app.kubernetes.io/instance={operator_name}"
    )
    # check if the console exists, if not do a helm upgrade
    console_pod = client.CoreV1Api(api_client=cluster_client).list_namespaced_pod(
        namespace, label_selector=f"app.kubernetes.io/instance=minio-operator-console"
    )
    if not operator_pod.items or not console_pod:
        print(f"Performing helm upgrade of minio-operator")

        helm_install_from_chart(
            release=operator_name,
            namespace=namespace,
            helm_args=helm_args,
            version=version,
            custom_repo=("minio", "https://operator.min.io/"),
            chart=f"minio/operator",
        )
    else:
        print(f"Minio operator already installed, skipping helm installation!")

    get_pod_when_ready(
        namespace,
        f"app.kubernetes.io/instance={operator_name}",
        api_client=cluster_client,
    )
    get_pod_when_ready(
        namespace,
        f"app.kubernetes.io/instance=minio-operator-console",
        api_client=cluster_client,
    )


def mino_tenant_install(
    namespace: str,
    tenant_name: str = MINIO_TENANT,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    helm_args: Dict[str, str] = None,
    version="5.0.6",
):
    if cluster_name is not None:
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    # check if the minio pod exists, if not do a helm upgrade
    pods = client.CoreV1Api(api_client=cluster_client).list_namespaced_pod(namespace, label_selector=f"app=minio")
    if not pods.items:
        print(f"Performing helm upgrade of minio-tenant")

        path = f"{Path(__file__).parent}/fixtures/minio/values-tenant.yaml"
        helm_install_from_chart(
            release=tenant_name,
            namespace=namespace,
            helm_args=helm_args,
            version=version,
            custom_repo=("minio", "https://operator.min.io/"),
            chart=f"minio/tenant",
            override_path=path,
        )
    else:
        print(f"Minio tenant already installed, skipping helm installation!")

    get_pod_when_ready(namespace, f"app=minio", api_client=cluster_client)


def get_appdb_member_cluster_names():
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]
