import kubernetes
from kubetester.awss3client import AwsS3Client
from pytest import fixture
from tests.common.constants import S3_BLOCKSTORE_NAME, S3_OPLOG_NAME
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket


@fixture(scope="module")
def s3_bucket_oplog(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    yield from create_s3_bucket_oplog(namespace, aws_s3_client, central_cluster_client)


def create_s3_bucket_oplog(namespace, aws_s3_client, api_client: kubernetes.client.ApiClient):
    create_aws_secret(aws_s3_client, S3_OPLOG_NAME + "-secret", namespace, api_client=api_client)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-oplog")


@fixture(scope="module")
def s3_bucket_blockstore(
    aws_s3_client: AwsS3Client,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    yield from create_s3_bucket_blockstore(namespace, aws_s3_client, api_client=central_cluster_client)


def create_s3_bucket_blockstore(namespace, aws_s3_client, api_client: kubernetes.client.ApiClient):
    create_aws_secret(aws_s3_client, S3_BLOCKSTORE_NAME + "-secret", namespace, api_client=api_client)
    yield from create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-blockstore")
