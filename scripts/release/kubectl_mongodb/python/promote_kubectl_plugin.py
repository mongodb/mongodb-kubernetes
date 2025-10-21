import argparse
import hashlib
import os
import subprocess
import sys
import tarfile
from pathlib import Path

import boto3
from botocore.exceptions import ClientError, NoCredentialsError, PartialCredentialsError
from github import Github, GithubException

from lib.base_logger import logger
from scripts.release.kubectl_mongodb.python.consts import *
from scripts.release.build.build_info import (
    load_build_info,
)

GITHUB_TOKEN = os.environ.get("GH_TOKEN")

S3_BUCKET_KUBECTL_PLUGIN_SUBPATH = KUBECTL_PLUGIN_BINARY_NAME

def main():
    release_version = os.environ.get("OPERATOR_VERSION")

    # figure out release and staging buckets using build_scenario
    build_scenario = os.environ.get("BUILD_SCENARIO")
    kubectl_plugin_build_info = load_build_info(build_scenario).binaries[KUBECTL_PLUGIN_BINARY_NAME]
    release_scenario_bucket_name = kubectl_plugin_build_info.s3_store

    download_artifacts_from_s3(release_version, get_commit_from_tag(release_version))

    notarize_artifacts(release_version)

    sign_and_verify_artifacts()

    artifacts_tar = create_tarballs()

    artifacts = generate_checksums(artifacts_tar)

    promote_artifacts(artifacts, release_version, release_scenario_bucket_name)

    upload_assets_to_github_release(artifacts, release_version)

# get_commit_from_tag gets the commit associated with a release tag, so that we can use that
# commit to pull the artifacts from staging bucket.
def get_commit_from_tag(tag: str) -> str:
    try:
        subprocess.run(
            ["git", "fetch", "--tags"],
            capture_output=True,
            text=True,
            check=True
        )

        result = subprocess.run(
            ["git", "rev-parse", "--short", f"{tag}^{{commit}}"], # git rev-parse v1.1.1^{commit}
            capture_output=True,
            text=True,
            check=True
        )
        return result.stdout.strip()

    except subprocess.CalledProcessError as e:
        logger.info(f"Failed to get commit for tag: {tag}, err: {e.stderr.strip()}")
        sys.exit(1)

# generate_checksums generates checksums for the artifacts that we are going to upload to github release as assets.
# It's formatted: checksum  artifact_name
def generate_checksums(artifacts: list[str]):
    checksums_path = Path(CHECKSUMS_PATH)

    with checksums_path.open("w") as out_file:
        for artifact in artifacts:
            artifact_path = Path(artifact)
            if not artifact_path.is_file() or not artifact_path.name.endswith(".tar.gz"):
                logger.info(f"skipping invalid tar file: {artifact_path}")
                continue

            sha256 = hashlib.sha256()
            with open(artifact_path, "rb") as f:
                # read chunk of 8192 bites until end of file (b"") is received
                for chunk in iter(lambda: f.read(8192), b""):
                    sha256.update(chunk)

            checksum_line = f"{sha256.hexdigest()}  {artifact_path.name}"
            out_file.write(checksum_line + "\n")

    logger.info(f"Checksums written to {checksums_path}")
    all_artifacts = list(artifacts) + [str(checksums_path.resolve())]
    return all_artifacts


# promote_artifacts promotes (copies) the downloaded staging artifacts to release S3 bucket.
def promote_artifacts(artifacts: list[str], release_version: str, release_scenario_bucket_name: str):
    s3_client = boto3.client("s3", region_name=AWS_REGION)
    for file in artifacts:
        if not os.path.isfile(file) or not file.endswith((".tar.gz", ".txt")):
            logger.info(f"Skipping invalid or non-tar/checksum file: {file}")
            continue

        file_name = os.path.basename(file)
        s3_key = os.path.join(S3_BUCKET_KUBECTL_PLUGIN_SUBPATH, release_version, file_name)

        try:
            s3_client.upload_file(file, release_scenario_bucket_name, s3_key)
        except ClientError as e:
            logger.debug(f"failed to upload the file {file}: {e}")
            sys.exit(1)

    logger.info("Artifacts were promoted to release bucket successfully")


# notarize_artifacts notarizes the darwin goreleaser binaries in-place.
def notarize_artifacts(release_version: str):
    notarize_result = subprocess.run(
        ["scripts/release/kubectl_mongodb/kubectl_mac_notarize.sh", release_version], capture_output=True, text=True
    )
    if notarize_result.returncode == 0:
        logger.info("Notarization of artifacts was successful")
    else:
        logger.debug(
            f"Notarization of artifacts failed. \nstdout: {notarize_result.stdout} \nstderr: {notarize_result.stderr}"
        )
        sys.exit(1)


# sign_and_verify_artifacts iterates over the goreleaser artifacts, that have been downloaded from S3, and
# signs and verifies them.
def sign_and_verify_artifacts():
    cwd = os.getcwd()
    artifacts_dir = os.path.join(cwd, LOCAL_ARTIFACTS_DIR)

    for subdir in os.listdir(artifacts_dir):
        subdir_path = os.path.join(artifacts_dir, subdir)

        # just work on dirs and not files
        if os.path.isdir(subdir_path):
            for file in os.listdir(subdir_path):
                file_path = os.path.join(subdir_path, file)

                if os.path.isfile(file_path):
                    # signing an already signed artifact fails with `Signature already exixts. Displaying proof`.
                    sign_result = subprocess.run(
                        ["scripts/release/kubectl_mongodb/sign.sh", file_path], capture_output=True, text=True
                    )
                    if sign_result.returncode == 0:
                        logger.info(f"Artifact {file_path} was signed successfully")
                    else:
                        logger.debug(
                            f"Signing the artifact {file_path} failed. \nstdout: {sign_result.stdout} \nstderr: {sign_result.stderr}"
                        )
                        sys.exit(1)

                    verify_result = subprocess.run(
                        ["scripts/release/kubectl_mongodb/verify.sh", file_path], capture_output=True, text=True
                    )
                    if verify_result.returncode == 0:
                        logger.info(f"Artifact {file_path} was verified successfully")
                    else:
                        logger.debug(
                            f"Verification of the artifact {file_path} failed. \nstdout: {verify_result.stdout} \nstderr: {verify_result.stderr}"
                        )
                        sys.exit(1)


def s3_artifacts_path_to_local_path(release_version: str, commit_sha: str):
    s3_common_path = f"{S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist"
    return {
        f"{s3_common_path}/kubectl-mongodb_darwin_amd64_v1/": f"kubectl-mongodb_{release_version}_darwin_amd64",
        f"{s3_common_path}/kubectl-mongodb_darwin_arm64/": f"kubectl-mongodb_{release_version}_darwin_arm64",
        f"{s3_common_path}/kubectl-mongodb_linux_amd64_v1/": f"kubectl-mongodb_{release_version}_linux_amd64",
        f"{s3_common_path}/kubectl-mongodb_linux_arm64/": f"kubectl-mongodb_{release_version}_linux_arm64",
        f"{s3_common_path}/kubectl-mongodb_linux_ppc64le/": f"kubectl-mongodb_{release_version}_linux_ppc64le",
        f"{s3_common_path}/kubectl-mongodb_linux_s390x/": f"kubectl-mongodb_{release_version}_linux_s390x",
    }


# download_artifacts_from_s3 downloads the staging artifacts (only that ones that we would later promote) from S3 and puts
# them in the local dir LOCAL_ARTIFACTS_DIR.
# ToDo: if the artifacts are not present at correct location, this is going to fail silently, we should instead fail this
def download_artifacts_from_s3(release_version: str, commit_sha: str):
    logger.info(f"Starting download of artifacts from staging S3 bucket: {STAGING_S3_BUCKET_NAME}")

    try:
        s3_client = boto3.client("s3", region_name=AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError):
        logger.debug("ERROR: AWS credentials were not set.")
        sys.exit(1)
    except Exception as e:
        logger.debug(f"An error occurred connecting to S3: {e}")
        sys.exit(1)

    artifacts_to_promote = s3_artifacts_path_to_local_path(release_version, commit_sha)

    # Create the local temporary directory if it doesn't exist
    os.makedirs(LOCAL_ARTIFACTS_DIR, exist_ok=True)

    download_count = 0
    for s3_artifact_dir, local_subdir in artifacts_to_promote.items():
        try:
            paginator = s3_client.get_paginator("list_objects_v2")
            pages = paginator.paginate(Bucket=STAGING_S3_BUCKET_NAME, Prefix=s3_artifact_dir)
            for page in pages:
                # "Contents" corresponds to the directory in the S3 bucket
                if "Contents" not in page:
                    continue
                for obj in page["Contents"]:
                    # obj is the S3 object in page["Contents"] directory
                    s3_key = obj["Key"]
                    if s3_key.endswith("/"):
                        # it's a directory
                        continue

                    # Get the path of the file relative to its S3 prefix, this would mostly be the object name itself
                    # if s3_artifact_dir doesn't container directories and has just the objects.
                    relative_path = os.path.relpath(s3_key, s3_artifact_dir)

                    final_local_path = os.path.join(LOCAL_ARTIFACTS_DIR, local_subdir, relative_path)

                    # Create the local directory structure if it doesn't exist
                    os.makedirs(os.path.dirname(final_local_path), exist_ok=True)

                    logger.info(f"Downloading staging artifact {s3_key} to {final_local_path}")
                    s3_client.download_file(STAGING_S3_BUCKET_NAME, s3_key, final_local_path)
                    download_count += 1

        except ClientError as e:
            logger.debug(f"ERROR: Failed to list or download from prefix '{s3_artifact_dir}'. S3 Client Error: {e}")
            return False

    if download_count == 0:
        logger.info(f"Couldn't download artifacts from staging S3 bucket {STAGING_S3_BUCKET_NAME}, please verify that artifacts are available under dir: {commit_sha}")
        sys.exit(1)

    logger.info("All the artifacts have been downloaded successfully.")
    return True


# create_tarballs creates `.tar.gz` archives for the artifacts that before promoting them.
def create_tarballs():
    logger.info(f"Creating archives for subdirectories in {LOCAL_ARTIFACTS_DIR}")
    created_archives = []
    original_cwd = os.getcwd()
    try:
        os.chdir(LOCAL_ARTIFACTS_DIR)

        for dir_name in os.listdir("."):
            if os.path.isdir(dir_name):
                archive_name = f"{dir_name}.tar.gz"

                with tarfile.open(archive_name, "w:gz") as tar:
                    tar.add(dir_name)

                full_archive_path = os.path.join(original_cwd, LOCAL_ARTIFACTS_DIR, archive_name)
                logger.info(f"Successfully created archive at {full_archive_path}")
                created_archives.append(full_archive_path)

    except Exception as e:
        logger.debug(f"ERROR: Failed to create tar.gz archives: {e}")
        sys.exit(1)
    finally:
        os.chdir(original_cwd)

    return created_archives


# upload_assets_to_github_release uploads the release artifacts (downloaded notarized/signed staging artifacts) to
# the github release as assets.
def upload_assets_to_github_release(asset_paths, release_version: str):
    if not GITHUB_TOKEN:
        logger.info("ERROR: GITHUB_TOKEN environment variable not set.")
        sys.exit(1)

    try:
        g = Github(GITHUB_TOKEN)
        repo = g.get_repo(GITHUB_REPO)
    except GithubException as e:
        logger.info(f"ERROR: Could not connect to GitHub or find repository '{GITHUB_REPO}', Error {e}.")
        sys.exit(1)

    try:
        release = repo.get_release(release_version)
    except GithubException as e:
        logger.debug(
            f"ERROR: Could not find release with tag '{release_version}'. Please ensure release exists already. Error: {e}"
        )
        sys.exit(2)

    for asset_path in asset_paths:
        asset_name = os.path.basename(asset_path)
        logger.info(f"Uploading artifact '{asset_name}' to github release as asset")
        try:
            release.upload_asset(path=asset_path, name=asset_name, content_type="application/gzip")
        except GithubException as e:
            logger.debug(f"ERROR: Failed to upload asset {asset_name}. Error: {e}")
            sys.exit(2)
        except Exception as e:
            logger.debug(f"An unexpected error occurred during upload of {asset_name}: {e}")
            sys.exit(2)


if __name__ == "__main__":
    main()
