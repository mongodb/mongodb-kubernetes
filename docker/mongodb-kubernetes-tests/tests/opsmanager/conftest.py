#!/usr/bin/env python3

import os
import time
from typing import List, Optional, Set

import boto3
from botocore.exceptions import ClientError
from kubernetes import client
from kubetester import get_pod_when_ready
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml as apply_yaml
from kubetester.kubetester import fixture as _fixture
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests import test_logger
from tests.conftest import is_multi_cluster

logger = test_logger.get_test_logger(__name__)


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


def _ensure_s3_buckets(
    endpoint: str,
    bucket_names: List[str],
    access_key: str = "rustfsadmin",
    secret_key: str = "rustfsadmin123",
    timeout: int = 120,
    interval: int = 5,
    issuer_ca_filepath: Optional[str] = None,
) -> None:
    """Ensure the S3 buckets exist via the S3 API (create each; already-exists is treated as success). Uses the test CA when the endpoint has custom TLS."""
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
                # S3 servers use HTTP 409 for bucket-already-exists.
                status_code = ce.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
                message_code = ce.response.get("Error", {}).get("Code", "")
                logger.debug(
                    "S3 bucket %s create: HTTP %s, Code=%s, %s",
                    bucket,
                    status_code,
                    message_code,
                    ce,
                )
                if status_code == 409:
                    ready.add(bucket)
            except Exception as e:
                logger.debug("S3 bucket create failed for %s (will retry): %s", bucket, e)
        if ready >= target:
            return
        time.sleep(interval)

    raise TimeoutError(f"Could not create S3 buckets within {timeout}s: missing {target - ready}")


RUSTFS_SERVICE_NAME = "rustfs"


def rustfs_install(
    namespace: str,
    issuer_ca_filepath: Optional[str] = None,
    timeout: int = 120,
) -> None:
    apply_yaml(client.ApiClient(), _fixture("rustfs.yaml"), namespace=namespace)
    get_pod_when_ready(namespace, f"app={RUSTFS_SERVICE_NAME}")
    _ensure_s3_buckets(
        endpoint=f"{RUSTFS_SERVICE_NAME}.{namespace}.svc.cluster.local",
        bucket_names=["s3-store-bucket", "oplog-s3-bucket"],
        issuer_ca_filepath=issuer_ca_filepath,
        timeout=timeout,
    )


def get_appdb_member_cluster_names():
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]
