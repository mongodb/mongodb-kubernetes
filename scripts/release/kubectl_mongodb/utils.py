import boto3
from botocore.exceptions import NoCredentialsError, PartialCredentialsError

from scripts.release.build.build_info import KUBECTL_PLUGIN_BINARY

AWS_REGION = "eu-north-1"

GITHUB_REPO = "mongodb/mongodb-kubernetes"

LOCAL_ARTIFACTS_DIR = "artifacts"
CHECKSUMS_PATH = f"{LOCAL_ARTIFACTS_DIR}/checksums.txt"


def create_s3_client() -> boto3.client:
    try:
        return boto3.client("s3", region_name=AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError) as e:
        raise Exception(f"Failed to create S3 client. AWS credentials not found: {e}")
    except Exception as e:
        raise Exception(f"An error occurred connecting to S3: {e}")


def parse_platform(platform) -> tuple[str, str]:
    return platform.split("/")


def kubectl_plugin_name(os_name: str, arch_name: str) -> str:
    return f"{KUBECTL_PLUGIN_BINARY}_{os_name}_{arch_name}"


# s3_path returns the path where the artifacts should be uploaded to in S3 object store.
# For dev workflows it's going to be `kubectl-mongodb/{evg-patch-id}/kubectl-mongodb_{goos}_{goarch}`,
# for staging workflows it would be `kubectl-mongodb/{commit-sha}/kubectl-mongodb_{goos}_{goarch}`.
# The `version` string has the correct version (either patch id or commit sha), based on the BuildScenario.
def s3_path(filename: str, version: str) -> str:
    return f"{KUBECTL_PLUGIN_BINARY}/{version}/{filename}"
