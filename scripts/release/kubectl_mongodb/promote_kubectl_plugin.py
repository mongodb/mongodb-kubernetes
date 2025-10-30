import hashlib
import os
import subprocess
import sys
import tarfile
from pathlib import Path

from botocore.exceptions import ClientError
from github import Github, GithubException

from lib.base_logger import logger
from scripts.release.build.build_info import (
    KUBECTL_PLUGIN_BINARY,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.kubectl_mongodb.download_kubectl_plugin import (
    download_kubectl_plugin_from_s3,
)
from scripts.release.kubectl_mongodb.utils import (
    CHECKSUMS_PATH,
    GITHUB_REPO,
    LOCAL_ARTIFACTS_DIR,
    create_s3_client,
    kubectl_plugin_name,
    parse_platform,
    s3_path,
)

GITHUB_TOKEN = os.environ.get("GH_TOKEN")


def main():
    release_version = os.environ.get("OPERATOR_VERSION")

    kubectl_plugin_release_info = load_build_info(BuildScenario.RELEASE).binaries[KUBECTL_PLUGIN_BINARY]
    release_scenario_bucket_name = kubectl_plugin_release_info.s3_store
    release_platforms = kubectl_plugin_release_info.platforms

    kubectl_plugin_staging_info = load_build_info(BuildScenario.STAGING).binaries[KUBECTL_PLUGIN_BINARY]
    staging_scenario_bucket_name = kubectl_plugin_staging_info.s3_store
    staging_version_override = os.environ.get("STAGING_VERSION_OVERRIDE")
    if staging_version_override:
        staging_version = staging_version_override
    else:
        staging_version = get_commit_from_tag(release_version)

    artifacts_dict = download_artifacts_from_s3(
        release_version, release_platforms, staging_version, staging_scenario_bucket_name
    )

    notarize_artifacts(release_version)

    sign_and_verify_artifacts(list(artifacts_dict.keys()))

    artifacts_tar = create_tarballs()

    checksum_file = generate_checksums(artifacts_tar)

    artifacts_tar_dict = {path: os.path.basename(path) for path in artifacts_tar}
    checksum_file_dict = {checksum_file: os.path.basename(checksum_file)}

    s3_artifacts = artifacts_dict | artifacts_tar_dict | checksum_file_dict
    promote_artifacts_to_s3(s3_artifacts, release_version, release_scenario_bucket_name)

    if os.environ.get("SKIP_GITHUB_RELEASE_UPLOAD", "false").lower() == "false":
        github_artifacts = artifacts_tar + [checksum_file]
        upload_assets_to_github_release(github_artifacts, release_version)


# get_commit_from_tag gets the commit associated with a release tag, so that we can use that
# commit to pull the artifacts from staging bucket.
def get_commit_from_tag(tag: str) -> str:
    try:
        subprocess.run(["git", "fetch", "--tags"], capture_output=True, text=True, check=True)

        result = subprocess.run(
            # using --short because that's how staging version is figured out for staging build scenario
            # https://github.com/mongodb/mongodb-kubernetes/blob/1.5.0/scripts/dev/contexts/evg-private-context#L137
            ["git", "rev-parse", "--short", f"{tag}^{{commit}}"],  # git rev-parse v1.1.1^{commit}
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()

    except subprocess.CalledProcessError as e:
        logger.info(f"Failed to get commit for tag: {tag}, err: {e.stderr.strip()}")
        sys.exit(1)


# generate_checksums generates checksums for the artifacts that we are going to upload to GitHub release as assets.
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
    return str(checksums_path.resolve())


# promote_artifacts promotes (copies) the downloaded staging artifacts to release S3 bucket.
def promote_artifacts_to_s3(artifacts: dict[str, str], release_version: str, release_scenario_bucket_name: str):
    s3_client = create_s3_client()

    for local_file_path, s3_file_name in artifacts.items():
        if not is_expected_artifact_for_promotion(local_file_path):
            logger.warning(f"Skipping invalid or non-tar/checksum artifact: {local_file_path}")
            continue

        s3_key = s3_path(s3_file_name, release_version)

        try:
            s3_client.upload_file(local_file_path, release_scenario_bucket_name, s3_key)
            logger.debug(f"{local_file_path} was promoted to s3://{release_scenario_bucket_name}/{s3_key} successfully")
        except ClientError as e:
            raise Exception(f"failed to upload the file {local_file_path}: {e}")

    logger.info("All artifacts were promoted to release bucket successfully")


def is_expected_artifact_for_promotion(file_path: str) -> bool:
    if not os.path.isfile(file_path):
        return False

    if file_path.endswith(".txt"):
        logger.debug(f"Promoting checksum file: {file_path}")
    elif file_path.endswith(".tar.gz"):
        logger.debug(f"Promoting tarball file: {file_path}")
    elif file_path.endswith(KUBECTL_PLUGIN_BINARY):
        logger.debug(f"Promoting binary required for e2e tests: {file_path}")
    else:
        return False

    return True


# notarize_artifacts notarizes the darwin binaries in-place.
def notarize_artifacts(release_version: str):
    notarize_result = subprocess.run(
        ["scripts/release/kubectl_mongodb/kubectl_mac_notarize.sh", release_version],
        capture_output=True,
        text=True,
    )
    if notarize_result.returncode == 0:
        print(notarize_result.stdout)
        logger.info("Notarization of artifacts was successful")
    else:
        logger.debug(
            f"Notarization of artifacts failed. \nstdout: {notarize_result.stdout} \nstderr: {notarize_result.stderr}"
        )
        sys.exit(1)


# sign_and_verify_artifacts iterates over the artifacts, that have been downloaded from S3, signs and verifies them.
def sign_and_verify_artifacts(artifacts: list[str]):
    for file_path in artifacts:
        # signing an already signed artifact fails with `Signature already exists. Displaying proof`.
        sign_result = subprocess.run(
            ["scripts/release/kubectl_mongodb/sign.sh", file_path], capture_output=True, text=True
        )
        if sign_result.returncode == 0:
            print(sign_result.stdout)
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
            print(verify_result.stdout)
            logger.info(f"Artifact {file_path} was verified successfully")
        else:
            logger.debug(
                f"Verification of the artifact {file_path} failed. \nstdout: {verify_result.stdout} \nstderr: {verify_result.stderr}"
            )
            sys.exit(1)


# download_artifacts_from_s3 downloads the staging artifacts (only that ones that we would later promote) from S3 and puts
# them in the local temp dir.
def download_artifacts_from_s3(
    release_version: str,
    release_platforms: list[str],
    staging_version: str,
    staging_s3_bucket_name: str,
) -> dict[str, str]:
    logger.info(f"Starting download of artifacts from staging S3 bucket: {staging_s3_bucket_name}")

    # Create the local temporary directory if it doesn't exist
    os.makedirs(LOCAL_ARTIFACTS_DIR, exist_ok=True)

    artifacts = {}
    for platform in release_platforms:
        os_name, arch_name = parse_platform(platform)

        local_plugin_dir = f"{KUBECTL_PLUGIN_BINARY}_{release_version}_{os_name}_{arch_name}"
        local_artifact_dir_path = os.path.join(LOCAL_ARTIFACTS_DIR, local_plugin_dir)
        os.makedirs(local_artifact_dir_path, exist_ok=True)
        local_artifact_file_path = os.path.join(local_artifact_dir_path, KUBECTL_PLUGIN_BINARY)

        s3_filename = kubectl_plugin_name(os_name, arch_name)
        staging_s3_plugin_path = s3_path(s3_filename, staging_version)

        download_kubectl_plugin_from_s3(staging_s3_bucket_name, staging_s3_plugin_path, local_artifact_file_path)
        artifacts[local_artifact_file_path] = s3_filename

    logger.info("All the artifacts have been downloaded successfully.")
    return artifacts


def set_permissions_filter(tarinfo):
    if tarinfo.name == "kubectl-mongodb":
        # This is the binary, make it executable: rwxr-xr-x
        tarinfo.mode = 0o755
    return tarinfo


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
                    # Iterate over the contents of the subdirectory (e.g., 'kubectl-mongodb_linux_s390x')
                    # and add them one by one.
                    for item_name in os.listdir(dir_name):
                        full_item_path = os.path.join(dir_name, item_name)
                        # Add just the binary (kubectl-mongodb_None_linux_s390x/kubectl-mongodb) to the tar
                        # instead of adding the dir.
                        # filter is passed to make the binary file executable
                        tar.add(full_item_path, arcname=item_name, filter=set_permissions_filter)

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
# the GitHub release as assets.
def upload_assets_to_github_release(asset_paths: list[str], release_version: str):
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
