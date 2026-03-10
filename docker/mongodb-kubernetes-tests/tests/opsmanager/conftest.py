#!/usr/bin/env python3

import os
import time
from pathlib import Path
from typing import Dict, List, Optional

import boto3
from botocore.exceptions import ClientError
from kubernetes import client
from kubetester import create_or_update_secret, get_pod_when_ready
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


def _create_minio_buckets(
    endpoint: str,
    bucket_names: List[str],
    access_key: str = "minio",
    secret_key: str = "minio123",
    timeout: int = 120,
    interval: int = 5,
    issuer_ca_filepath: Optional[str] = os.getenv("MINIO_ISSUER_CA_FILEPATH", None),
) -> None:
    """Create MinIO buckets via S3 API when the operator cannot (e.g. custom TLS).

    The MinIO operator creates buckets by connecting to the tenant over HTTPS. When
    the tenant uses custom certs (tls-ssl-minio) signed by the test CA, the operator
    does not trust that CA and fails with "x509: certificate signed by unknown
    authority". This helper creates the buckets from the test using the test CA for
    verification, so the rest of the test can proceed.
    """
    s3 = boto3.client(
        "s3",
        endpoint_url=f"https://{endpoint}",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        verify=issuer_ca_filepath,
    )
    deadline = time.time() + timeout
    created = set()
    created_via_boto = set()
    while time.time() < deadline:
        for bucket in bucket_names:
            if bucket in created:
                continue
            try:
                s3.head_bucket(Bucket=bucket)
                created.add(bucket)
                print(f"MinIO bucket '{bucket}' already exists (created by MinIO operator)")
            except ClientError as e:
                code = e.response["Error"].get("Code", "")
                if code in ("404", "NoSuchBucket"):
                    try:
                        s3.create_bucket(Bucket=bucket)
                        created.add(bucket)
                        created_via_boto.add(bucket)
                        print(f"MinIO bucket '{bucket}' created via boto3")
                    except ClientError as create_err:
                        if create_err.response["Error"].get("Code") == "BucketAlreadyOwnedByYou":
                            created.add(bucket)
                            print(f"MinIO bucket '{bucket}' already exists (created by MinIO operator)")
                        pass
                else:
                    raise
            except Exception:
                # Connection refused, SSL, timeout - MinIO may not be ready yet; retry
                pass
        if len(created) == len(bucket_names):
            via_boto = sorted(created_via_boto)
            via_minio = sorted(set(bucket_names) - created_via_boto)
            if via_boto:
                print(f"MinIO buckets created via boto3: {via_boto}")
            if via_minio:
                print(f"MinIO buckets already present (created by MinIO operator): {via_minio}")
            return
        time.sleep(interval)
    missing = set(bucket_names) - created
    raise TimeoutError(f"Could not create MinIO buckets within {timeout}s: missing {missing}")


def _wait_for_minio_buckets(
    endpoint: str,
    bucket_names: List[str],
    access_key: str = "minio",
    secret_key: str = "minio123",
    timeout: int = 500,
    interval: int = 10,
    issuer_ca_filepath: Optional[str] = os.getenv("MINIO_ISSUER_CA_FILEPATH", None),
):
    """Poll S3/MinIO until all buckets are accessible or timeout is reached.

    Pod readiness does not guarantee bucket provisioning is complete. This
    function bridges the gap by probing headBucket() with retry/backoff,
    mirroring the exact check OpsManager performs when saving S3 store config.
    """
    s3 = boto3.client(
        "s3",
        endpoint_url=f"https://{endpoint}",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        verify=issuer_ca_filepath,
    )

    deadline = time.time() + timeout
    pending = set(bucket_names)

    while time.time() < deadline:
        for bucket in list(pending):
            try:
                s3.head_bucket(Bucket=bucket)
                print(f"MinIO bucket '{bucket}' is accessible")
                pending.discard(bucket)
            except ClientError as e:
                code = e.response["Error"]["Code"]
                print(f"MinIO bucket '{bucket}' not ready (HTTP {code}), retrying in {interval}s...")
        if not pending:
            print(f"All MinIO buckets accessible: {bucket_names}")
            return
        time.sleep(interval)

    raise TimeoutError(f"MinIO buckets still inaccessible after {timeout}s: {pending}")


def mino_tenant_install(
    namespace: str,
    tenant_name: str = MINIO_TENANT,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
    helm_args: Dict[str, str] = None,
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
    # Create buckets from the test so we don't rely on the operator. The operator creates
    # buckets over HTTPS; with custom certs (tls-ssl-minio) it often fails with x509 or
    # connection refused (race with MinIO readiness), so bucket creation is flaky. Creating
    # them here with the test CA makes the test stable.
    bucket_names = ["s3-store-bucket", "oplog-s3-bucket"]
    _create_minio_buckets(
        endpoint=f"minio.{namespace}.svc.cluster.local",
        bucket_names=bucket_names,
        issuer_ca_filepath=issuer_ca_filepath,
    )
    _wait_for_minio_buckets(
        endpoint=f"minio.{namespace}.svc.cluster.local",
        bucket_names=bucket_names,
        issuer_ca_filepath=issuer_ca_filepath,
    )


def get_appdb_member_cluster_names():
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]
