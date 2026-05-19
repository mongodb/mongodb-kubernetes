#!/usr/bin/env python3

import os
import time
from pathlib import Path
from typing import Dict, List, Optional, Set

import boto3
from botocore.exceptions import ClientError
from kubernetes import client
from kubetester import create_or_update_secret, get_pod_when_ready
from kubetester.helm import helm_install_from_chart
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests import test_logger
from tests.conftest import is_multi_cluster

logger = test_logger.get_test_logger(__name__)

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
    helm_args: Optional[Dict[str, str]] = None,
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


def _create_minio_buckets(
    endpoint: str,
    bucket_names: List[str],
    access_key: str = "minio",
    secret_key: str = "minio123",
    timeout: int = 120,
    interval: int = 5,
    issuer_ca_filepath: Optional[str] = None,
) -> None:
    """Ensure MinIO buckets exist via S3 API (create each; already-exists is treated as success). Uses test CA when tenant has custom TLS."""
    s3 = boto3.client(
        "s3",
        endpoint_url=f"https://{endpoint}",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        verify=issuer_ca_filepath,
    )
    target: Set[str] = set(bucket_names)
    ready: Set[str] = set()
    deadline = time.time() + timeout

    while time.time() < deadline:
        for bucket in bucket_names:
            if bucket in ready:
                continue
            try:
                s3.create_bucket(Bucket=bucket)
                ready.add(bucket)
            except ClientError as ce:
                # boto3 ClientError.response: ResponseMetadata.HTTPStatusCode, Error.Code
                # https://boto3.amazonaws.com/v1/documentation/api/latest/guide/error-handling.html
                # MinIO/S3 use HTTP 409 for bucket-already-exists.
                status_code = ce.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
                message_code = ce.response.get("Error", {}).get("Code", "")
                logger.debug(
                    "MinIO bucket %s create: HTTP %s, Code=%s, %s",
                    bucket,
                    status_code,
                    message_code,
                    ce,
                )
                if status_code == 409:
                    ready.add(bucket)
            except Exception as e:
                logger.debug("MinIO bucket create failed for %s (will retry): %s", bucket, e)
                # MinIO not ready (connection/SSL) or transient S3 errors, retry
        if ready >= target:
            return
        time.sleep(interval)

    raise TimeoutError(f"Could not create MinIO buckets within {timeout}s: missing {target - ready}")


def mino_tenant_install(
    namespace: str,
    tenant_name: str = MINIO_TENANT,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    helm_args: Optional[Dict[str, str]] = None,
    version="5.0.6",
    issuer_ca_filepath: Optional[str] = os.getenv("MINIO_ISSUER_CA_FILEPATH", None),
):
    if cluster_name is not None:
        os.environ["HELM_KUBECONTEXT"] = cluster_name

    if helm_args is None:
        helm_args = {}

    # check if the minio pod exists, if not do a helm upgrade
    pods = client.CoreV1Api(api_client=cluster_client).list_namespaced_pod(namespace, label_selector=f"app=minio")
    if not pods.items:
        print(f"Performing helm upgrade of minio-tenant")

        # Provide the test CA to the MinIO operator so it can trust the MinIO server's
        # TLS cert when creating buckets. Without this, the operator fails with
        # "x509: certificate signed by unknown authority" and buckets are never created.
        #
        # We use ca-tls.crt (the bare test CA) rather than issuer_ca_filepath
        # (ca-tls-full-chain.crt). The full-chain file bundles extra MongoDB CDN certs that
        # have since expired. The MinIO operator (Go x509) marks the entire secret as expired
        # if any cert inside it is expired, so even the still-valid test CA would be skipped.
        if issuer_ca_filepath is not None:
            ca_cert_path = Path(issuer_ca_filepath).parent / "ca-tls.crt"
            with open(ca_cert_path) as f:
                ca_cert = f.read()
            create_or_update_secret(
                namespace=namespace,
                name="minio-ca-cert",
                data={"public.crt": ca_cert},
                api_client=cluster_client,
            )
            helm_args["tenant.certificate.externalCaCertSecret[0].name"] = "minio-ca-cert"

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
    # Ensure buckets exist from the test so we don't rely on the operator (custom TLS often
    # breaks operator bucket creation). Retries until all buckets are created/accessible.
    _create_minio_buckets(
        endpoint=f"minio.{namespace}.svc.cluster.local",
        bucket_names=["s3-store-bucket", "oplog-s3-bucket"],
        issuer_ca_filepath=issuer_ca_filepath,
    )


def get_appdb_member_cluster_names():
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]
