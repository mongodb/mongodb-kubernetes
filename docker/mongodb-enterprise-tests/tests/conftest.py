import os

from kubetester.awss3client import AwsS3Client
from pytest import fixture

import kubernetes


try:
    kubernetes.config.load_kube_config()
except Exception:
    kubernetes.config.load_incluster_config()


@fixture(scope="module")
def namespace() -> str:
    namespace = os.getenv("PROJECT_NAMESPACE", None)

    if namespace is None:
        raise Exception("PROJECT_NAMESPACE needs to be defined")

    return namespace


@fixture(scope="module")
def aws_s3_client() -> AwsS3Client:
    return AwsS3Client("us-east-1")
