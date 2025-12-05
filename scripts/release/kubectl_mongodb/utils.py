import os
import sys

import boto3
from botocore.exceptions import NoCredentialsError, PartialCredentialsError
from github import Github, GithubException

from lib.base_logger import logger
from scripts.release.build.build_info import KUBECTL_PLUGIN_BINARY

AWS_REGION = "eu-north-1"

GITHUB_REPO = "mongodb/mongodb-kubernetes"
GITHUB_TOKEN = os.environ.get("GH_TOKEN")

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


# upload_assets_to_github_release uploads the release artifacts (downloaded notarized/signed staging artifacts) to
# the GitHub release as assets.
def upload_assets_to_github_release(asset_paths: list[str], release_version: str):
    if not GITHUB_TOKEN:
        raise Exception("ERROR: GITHUB_TOKEN environment variable not set.")

    try:
        g = Github(GITHUB_TOKEN)
        repo = g.get_repo(GITHUB_REPO)
    except GithubException as e:
        raise Exception(f"ERROR: Could not connect to GitHub or find repository {GITHUB_REPO}") from e

    try:
        gh_release = None
        # list all the releases (including draft ones), and get the one corresponding to the passed release_version
        for r in repo.get_releases():
            if r.tag_name == release_version:
                gh_release = r
                break

        if gh_release is None:
            raise Exception(
                f"Could not find release (published or draft) with tag '{release_version}'. Please ensure the release exists."
            )
    except GithubException as e:
        raise Exception(f"Failed to retrieve releases from the repository {GITHUB_REPO}") from e

    for asset_path in asset_paths:
        asset_name = os.path.basename(asset_path)
        logger.info(f"Uploading artifact '{asset_name}' to github release as asset")
        try:
            gh_release.upload_asset(path=asset_path, name=asset_name, content_type="application/gzip")
        except GithubException as e:
            raise Exception(f"ERROR: Failed to upload asset {asset_name}") from e
        except Exception as e:
            raise Exception(f"An unexpected error occurred during upload of {asset_name}") from e
