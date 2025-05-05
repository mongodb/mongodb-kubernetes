"""SBOM manipulation library

This file contains all necessary functions for manipulating SBOMs for MCK. The intention is to run
generate_sbom and generate_sbom_for_cli on a daily basis per each shipped image and the CLI.

The SSDLC reporting doesn't strictly require to follow the daily rebuild flow. However, triggering it is part of the
release process, and it might be used in the future (perceived security vs real security). More information about the
report generation might be found in https://wiki.corp.mongodb.com/display/MMS/Kubernetes+Enterprise+Operator+Release+Guide#KubernetesEnterpriseOperatorReleaseGuide-SSDLC

On a typical daily run, the workflow is the following:

- Generate SBOM Lite
- Uploading SBOM Lite to Kondukto
- Generate Augmented SBOM
- Uploading SBOM Lite and Augmented SBOM to S3

In addition to this, there are special steps done only for the initial upload of a newly released images:

- Generate SBOM Lite
- Uploading SBOM Lite to Kondukto
- Generate Augmented SBOM
- Uploading the SBOM Lite and Augmented SBOM to a special path to S3, it's never updated - we want it to stay the same

"""

import os
import random
import subprocess
import tempfile
import time
import urllib

import boto3
import botocore

from lib.base_logger import logger

S3_BUCKET = "kubernetes-operators-sboms"
SILK_BOMB_IMAGE = "artifactory.corp.mongodb.com/release-tools-container-registry-public-local/silkbomb:2.0"
KONDUKTO_REPO = "mongodb/mongodb-kubernetes"
WORKDIR = os.getenv("workdir")


def get_image_sha(image_pull_spec: str):
    logger.debug(f"Finding image SHA for {image_pull_spec}")
    # Because of the manifest generation workflow, the Docker Daemon might be confused what have been built
    # locally and what not. We need re-pull the image to ensure it's fresh every time we obtain the SHA Digest.
    command = [
        "docker",
        "pull",
        image_pull_spec,
    ]
    subprocess.run(command, check=True, capture_output=True, text=True)
    # See https://stackoverflow.com/a/55626495
    command = [
        "docker",
        "inspect",
        "--format={{index .Id}}",
        image_pull_spec,
    ]
    result = subprocess.run(command, check=True, capture_output=True, text=True)
    logger.debug(f"Found image SHA")
    return result.stdout.strip()


def parse_image_pull_spec(image_pull_spec: str):
    logger.debug(f"Parsing image pull spec {image_pull_spec}")

    parts = image_pull_spec.split("/")

    registry = parts[0]
    organization = parts[1]
    image_name = parts[2]

    image_parts = image_name.split(":")
    image_name = image_parts[0]
    tag = image_parts[1]
    sha = get_image_sha(image_pull_spec)

    logger.debug(
        f"Parsed image spec, registry: {registry}, org: {organization}, image: {image_name}, tag: {tag}, sha: {sha}"
    )
    return registry, organization, image_name, tag, sha


def create_sbom_lite_for_image(image_pull_spec: str, directory: str, file_name: str, platform: str):
    logger.debug(f"Creating SBOM for {image_pull_spec} to {directory}/{file_name}")
    command = [
        "docker",
        "sbom",
        "--platform",
        platform,
        "-o",
        f"{directory}/{file_name}",
        "--format",
        "cyclonedx-json",
        image_pull_spec,
    ]
    subprocess.run(command, check=True)
    logger.debug(f"Created SBOM")


def upload_to_s3(directory: str, file_name: str, s3_bucket: str, s3_path: str):
    file_on_disk = f"{directory}/{file_name}"
    logger.debug(f"Uploading file {file_on_disk} to S3 {s3_bucket}/{s3_path}")
    s3 = boto3.resource("s3")
    versioning = s3.BucketVersioning(s3_bucket)
    versioning.enable()
    s3.meta.client.upload_file(file_on_disk, S3_BUCKET, s3_path)
    logger.debug(f"Uploading file done")


def get_silkbomb_env_file_path() -> str:
    if not WORKDIR:
        raise EnvironmentError("'workdir' environment variable is not set")

    silkbomb_env_path = os.path.join(WORKDIR, "silkbomb.env")
    if not os.path.exists(silkbomb_env_path):
        raise FileNotFoundError(f"{silkbomb_env_path} does not exist")
    return silkbomb_env_path


def augment_sbom(
    silkbomb_env_file: str,
    directory: str,
    sbom_lite_file_name: str,
    sbom_augmented_file_name: str,
    kondukto_repo: str,
    kondukto_branch: str,
) -> bool:
    logger.debug(f"Augmenting SBOM {directory}/{sbom_lite_file_name} with Kondukto scan results")

    command = [
        "docker",
        "run",
        "--platform",
        "linux/amd64",
        "--rm",
        "-v",
        f"{directory}:/sboms",
        "--env-file",
        silkbomb_env_file,
        SILK_BOMB_IMAGE,
        "augment",
        "--sbom_in",
        f"sboms/{sbom_lite_file_name}",
        "--repo",
        kondukto_repo,
        "--branch",
        kondukto_branch,
        "--sbom_out",
        f"sboms/{sbom_augmented_file_name}",
    ]

    logger.debug(f"Calling Silkbomb augment: {' '.join(command)}")
    if retry(lambda: subprocess.run(command, check=True)):
        logger.debug(f"Augmenting SBOM done")
        return True
    else:
        logger.error(f"Failed to augment SBOM")
        return False


def retry(f, max_retries=5) -> bool:
    for attempt in range(max_retries):
        try:
            logger.debug(f"Calling function with retries")
            f()
            logger.debug(f"Calling function with retries done")
            return True
        except subprocess.CalledProcessError as e:
            err = e
            wait_time = (2**attempt) + random.uniform(0, 1)
            logger.warning(f"Rate limited. Retrying in {wait_time:.2f} seconds...")
            time.sleep(wait_time)
    logger.error(f"Calling function with retries failed with error: {err}")
    return False


def download_file(url: str, directory: str, file_path: str):
    logger.info(f"Downloading file {directory}/{file_path} from {url}")
    urllib.request.urlretrieve(url, f"{directory}/{file_path}")
    logger.info("Downloading file done")


def unpack(directory: str, file_path: str):
    logger.info(f"Unpacking {directory}/{file_path}")
    subprocess.check_output(f"tar -zxf {directory}/{file_path} -C {directory}", shell=True)
    logger.info("Unpacking done")


def create_sbom_lite_for_binary(directory: str, file_path: str, sbom_light_path: str):
    logger.info(f"Creating SBOM Lite for {directory}/{file_path}")

    purl_file_name = f"{sbom_light_path}.purl"

    subprocess.check_call(
        f"./scripts/evergreen/release/purl_creator.sh {directory}/{file_path} {directory}/{purl_file_name}", shell=True
    )

    command = [
        "docker",
        "run",
        "--platform",
        "linux/amd64",
        "--rm",
        "-v",
        f"{directory}:/sboms",
        SILK_BOMB_IMAGE,
        "update",
        "--purls",
        f"/sboms/{purl_file_name}",
        "--sbom_out",
        f"/sboms/{sbom_light_path}",
    ]
    logger.debug(f"Calling update purls: {' '.join(command)}")
    subprocess.run(command, check=True)

    logger.info(f"Creating SBOM Lite done")


def generate_sbom_for_cli(cli_version: str = "1.25.0", platform: str = "linux/amd64"):
    logger.info(f"Generating SBOM for CLI for version {cli_version} and platform {platform}")
    try:
        silkbomb_env_file = get_silkbomb_env_file_path()
        platform_sanitized = platform.replace("/", "-")
        platform_sanitized_with_underscores = platform.replace("/", "_")

        with tempfile.TemporaryDirectory() as directory:
            sbom_lite_file_name = f"kubectl-mongodb-{cli_version}-{platform_sanitized}.json"
            sbom_augmented_file_name = f"kubectl-mongodb-{cli_version}-{platform_sanitized}-augmented.json"
            product_name = "mongodb-kubernetes-cli"
            kondukto_project_repo = "mongodb/mongodb-kubernetes"
            kondukto_branch_id = f"{product_name}-release-{cli_version}-{platform_sanitized}"
            s3_release_sbom_lite_path = f"sboms/release/lite/{product_name}/{cli_version}/{platform_sanitized}"
            s3_release_sbom_augmented_path = (
                f"sboms/release/augmented/{product_name}/{cli_version}/{platform_sanitized}"
            )
            binary_file_name = f"kubectl-mongodb_{cli_version}_{platform_sanitized_with_underscores}.tar.gz"
            download_binary_url = (
                f"https://github.com/mongodb/mongodb-kubernetes/releases/download/{cli_version}/{binary_file_name}"
            )
            unpacked_binary_file_name = "kubectl-mongodb"

            if not s3_path_exists(s3_release_sbom_augmented_path):
                download_file(download_binary_url, directory, binary_file_name)
                unpack(directory, binary_file_name)
                create_sbom_lite_for_binary(directory, unpacked_binary_file_name, sbom_lite_file_name)
                logger.info("Augmenting SBOM Lite and uploading SBOM Lite and Augmented SBOM for the first release")
                if augment_sbom(
                    silkbomb_env_file,
                    directory,
                    sbom_lite_file_name,
                    sbom_augmented_file_name,
                    kondukto_project_repo,
                    kondukto_branch_id,
                ):
                    upload_to_s3(directory, sbom_lite_file_name, S3_BUCKET, s3_release_sbom_lite_path)
                    upload_to_s3(directory, sbom_augmented_file_name, S3_BUCKET, s3_release_sbom_augmented_path)
                else:
                    logger.exception(f"Could not augment release SBOM with Kondukto scan results")
    except:
        logger.exception("Skipping SBOM Generation because of an error")

    logger.info(f"Generating SBOM done")


def get_kondukto_sbom_data(image_name: str, tag: str, platform_sanitized: str):
    daily_project_branch_id = f"{image_name}-daily-{tag}-{platform_sanitized}"
    release_project_branch_id = f"{image_name}-release-{tag}-{platform_sanitized}"
    return daily_project_branch_id, release_project_branch_id, "mongodb/mongodb-kubernetes"


def s3_path_exists(s3_path):
    logger.debug(f"Checking if path exists {s3_path} ?")
    pathExists = False
    s3 = boto3.client("s3")
    try:
        response = s3.list_objects(Bucket=S3_BUCKET, Prefix=s3_path, MaxKeys=1)
        logger.debug(f"Response from S3: {response}")
        if "Contents" in response:
            logger.debug(f"Content found, assuming the path exists")
            pathExists = True
    except botocore.exceptions.ClientError as e:
        if e.response["Error"]["Code"] != "404":
            logger.exception("Could not determine if the path exists. Assuming it is not.")
    logger.debug(f"Checking done ({pathExists})")
    return pathExists


def generate_sbom(image_pull_spec: str, platform: str = "linux/amd64"):
    logger.info(f"Generating SBOM for {image_pull_spec} {platform}")

    registry: str
    organization: str
    image_name: str
    tag: str
    try:
        silkbomb_env_file = get_silkbomb_env_file_path()
        registry, organization, image_name, tag, sha = parse_image_pull_spec(image_pull_spec)
        platform_sanitized = platform.replace("/", "-")
        daily_project_branch_id, release_project_branch_id, kondukto_project_repo = get_kondukto_sbom_data(
            image_name, tag, platform_sanitized
        )

        with tempfile.TemporaryDirectory() as directory:
            sbom_lite_file_name = f"{image_name}_{tag}_{platform_sanitized}.json"
            sbom_augmented_file_name = f"{image_name}_{tag}_{platform_sanitized}-augmented.json"

            create_sbom_lite_for_image(image_pull_spec, directory, sbom_lite_file_name, platform)

            ### Daily SBOM generation ###
            s3_daily_sbom_lite_path = (
                f"sboms/daily/lite/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}/{sha}"
            )
            s3_daily_sbom_augmented_path = (
                f"sboms/daily/augmented/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}/{sha}"
            )

            # produce Augmented SBOM with Silkbomb and upload SBOM Lite and Augmented SBOM to S3
            if augment_sbom(
                silkbomb_env_file,
                directory,
                sbom_lite_file_name,
                sbom_augmented_file_name,
                kondukto_project_repo,
                daily_project_branch_id,
            ):
                upload_to_s3(directory, sbom_lite_file_name, S3_BUCKET, s3_daily_sbom_lite_path)
                upload_to_s3(directory, sbom_augmented_file_name, S3_BUCKET, s3_daily_sbom_augmented_path)
            else:
                logger.exception(f"Could not augment daily SBOM with Kondukto scan results. Continuing...")

            ### Release SBOM generation ###
            # Then checking for path, we don't want to include SHA Digest.
            # We just want to keep there the initial one. Nothing more.
            s3_release_sbom_augmented_path_for_specific_tag = (
                f"sboms/release/augmented/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}/"
            )

            s3_release_sbom_lite_path = (
                f"sboms/release/lite/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}/{sha}"
            )
            s3_release_sbom_augmented_path = (
                f"sboms/release/augmented/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}/{sha}"
            )

            # This path is only executed when there's a first rebuild of the release artifacts.
            # Then, we upload the SBOM Lite and Augmented SBOM this single time only.
            if not s3_path_exists(s3_release_sbom_augmented_path_for_specific_tag):
                logger.info("Augmenting SBOM Lite and uploading SBOM Lite and Augmented SBOM for the first release")
                if augment_sbom(
                    silkbomb_env_file,
                    directory,
                    sbom_lite_file_name,
                    sbom_augmented_file_name,
                    kondukto_project_repo,
                    release_project_branch_id,
                ):
                    upload_to_s3(directory, sbom_lite_file_name, S3_BUCKET, s3_release_sbom_lite_path)
                    upload_to_s3(directory, sbom_augmented_file_name, S3_BUCKET, s3_release_sbom_augmented_path)
                else:
                    logger.exception(f"Could not augment release SBOM with Kondukto scan results")

    except Exception as err:
        logger.exception(f"Skipping SBOM Generation because of an error: {err}")

    logger.info(f"Generating SBOM done")
