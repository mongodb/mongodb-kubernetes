"""SBOM manipulation library

This file contains all necessary functions for manipulating SBOMs for MCO and MEKO. The intention is to run
generate_sbom and generate_sbom_for_cli on a daily basis per each shipped image and the CLI.

During each run, the script does the following:

- Generates SBOM Lite using docker sbom
- Uploads it S3
- Uploads it to Silk
- Downloads an Augmented SBOM from Silk (augmentation happens on demand during download)
- Uploads Augmented SBOM to S3
"""

import os
import subprocess
import tempfile
import urllib

import boto3

from scripts.evergreen.release.base_logger import logger
from scripts.evergreen.release.images_signing import mongodb_artifactory_login

S3_BUCKET = "kubernetes-operators-sboms"
SILK_BOMB_IMAGE = "artifactory.corp.mongodb.com/release-tools-container-registry-local/silkbomb:1.0"


def parse_image_pull_spec(image_pull_spec: str):
    logger.debug(f"Parsing image pull spec {image_pull_spec}")

    parts = image_pull_spec.split("/")

    registry = parts[0]
    organization = parts[1]
    image_name = parts[2]

    image_parts = image_name.split(":")
    image_name = image_parts[0]
    tag = image_parts[1]

    logger.debug(f"Parsed image spec, registry: {registry}, org: {organization}, image: {image_name}, tag: {tag}")
    return registry, organization, image_name, tag


def create_sbom_light_for_image(image_pull_spec: str, directory: str, file_name: str, platform: str):
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


def validate_environment():
    if "ARTIFACTORY_USERNAME" not in os.environ:
        raise ValueError("ARTIFACTORY_USERNAME not defined in environment")
    if "ARTIFACTORY_PASSWORD" not in os.environ:
        raise ValueError("ARTIFACTORY_PASSWORD not defined in environment")
    if "SILK_CLIENT_ID" not in os.environ:
        raise ValueError("SILK_CLIENT_ID not defined in environment")
    if "SILK_CLIENT_SECRET" not in os.environ:
        raise ValueError("SILK_CLIENT_SECRET not defined in environment")


def upload_sbom_lite_to_silk(directory: str, file_name: str, asset_group: str, platform: str):
    logger.debug(f"Uploading SBOM Lite {directory}/{file_name} to Silk")
    mongodb_artifactory_login()
    silk_client_id = os.getenv("SILK_CLIENT_ID")
    silk_client_secret = os.getenv("SILK_CLIENT_SECRET")

    command = [
        "docker",
        "run",
        "--platform",
        "linux/amd64",
        "--rm",
        "-v",
        f"{directory}:/sboms",
        "-e",
        f"SILK_CLIENT_ID={silk_client_id}",
        "-e",
        f"SILK_CLIENT_SECRET={silk_client_secret}",
        SILK_BOMB_IMAGE,
        "upload",
        "--silk_asset_group",
        asset_group,
        "--sbom_in",
        f"sboms/{file_name}",
    ]
    logger.debug(f"Calling Silk upload: {' '.join(command)}")
    subprocess.run(command, check=True)
    logger.debug(f"Uploading SBOM Lite done")


def download_augmented_sbom_from_silk(directory: str, file_name: str, asset_group: str, platform: str):
    logger.debug(f"Downloading Augmented SBOM {directory}/{file_name} from Silk")
    silk_client_id = os.getenv("SILK_CLIENT_ID")
    silk_client_secret = os.getenv("SILK_CLIENT_SECRET")
    command = [
        "docker",
        "run",
        "--platform",
        "linux/amd64",
        "--rm",
        "-v",
        f"{directory}:/sboms",
        "-e",
        f"SILK_CLIENT_ID={silk_client_id}",
        "-e",
        f"SILK_CLIENT_SECRET={silk_client_secret}",
        SILK_BOMB_IMAGE,
        "download",
        "--silk_asset_group",
        asset_group,
        "--sbom_out",
        f"sboms/{file_name}",
    ]
    logger.debug(f"Calling Silk download: {' '.join(command)}")
    subprocess.run(command, check=True)
    logger.debug(f"Downloading Augmented SBOM done")


def download_file(url: str, directory: str, file_path: str):
    logger.info(f"Downloading file {directory}/{file_path} from {url}")
    urllib.request.urlretrieve(url, f"{directory}/{file_path}")
    logger.info("Downloading file done")


def unpack(directory: str, file_path: str):
    logger.info(f"Unpacking {directory}/{file_path}")
    subprocess.check_output(f"tar -zxf {directory}/{file_path} -C {directory}", shell=True)
    logger.info("Unpacking done")


def create_sbom_light_for_binary(directory: str, file_path: str, sbom_light_path: str, platform: str):
    logger.info(f"Creating SBOM Light for {directory}/{file_path}")

    purl_file_name = f"{sbom_light_path}.purl"

    subprocess.check_call(
        f"./scripts/evergreen/release/purl_creator.sh {directory}/{file_path} {directory}/{purl_file_name}", shell=True
    )

    mongodb_artifactory_login()

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

    logger.info(f"Creating SBOM Light done")


def generate_sbom_for_cli(cli_version: str = "1.25.0", platform: str = "linux/amd64"):
    logger.info(f"Generating SBOM for CLI for version {cli_version} and platform {platform}")
    try:
        validate_environment()
        platform_sanitized = platform.replace("/", "-")
        platform_sanitized_with_underscores = platform.replace("/", "_")

        with tempfile.TemporaryDirectory() as directory:
            sbom_lite_file_name = f"kubectl-mongodb-{cli_version}-{platform_sanitized}.json"
            sbom_augmented_file_name = f"kubectl-mongodb-{cli_version}-{platform_sanitized}-augmented.json"
            product_name = "mongodb-enterprise-cli"
            asset_name = f"{product_name}-{platform_sanitized}"
            s3_sbom_lite_path = f"sboms/lite/{product_name}/{cli_version}/{platform_sanitized}"
            s3_sbom_augmented_path = f"sboms/augmented/{product_name}/{cli_version}/{platform_sanitized}"
            binary_file_name = f"kubectl-mongodb_{cli_version}_{platform_sanitized_with_underscores}.tar.gz"
            download_binary_url = f"https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/download/{cli_version}/{binary_file_name}"
            unpacked_binary_file_name = "kubectl-mongodb"

            download_file(download_binary_url, directory, binary_file_name)
            unpack(directory, binary_file_name)
            create_sbom_light_for_binary(directory, unpacked_binary_file_name, sbom_lite_file_name, platform)
            upload_to_s3(directory, sbom_lite_file_name, S3_BUCKET, s3_sbom_lite_path)
            upload_sbom_lite_to_silk(directory, sbom_lite_file_name, asset_name, platform)
            download_augmented_sbom_from_silk(directory, sbom_augmented_file_name, asset_name, platform)
            upload_to_s3(directory, sbom_augmented_file_name, S3_BUCKET, s3_sbom_augmented_path)
    except:
        logger.exception("Skipping SBOM Generation because of an error")

    logger.info(f"Generating SBOM done")


def generate_sbom(image_pull_spec: str, platform: str = "linux/amd64"):
    logger.info(f"Generating SBOM for {image_pull_spec}")

    registry: str
    organization: str
    image_name: str
    tag: str
    try:
        validate_environment()
        registry, organization, image_name, tag = parse_image_pull_spec(image_pull_spec)
        platform_sanitized = platform.replace("/", "-")

        with tempfile.TemporaryDirectory() as directory:
            sbom_lite_file_name = f"{image_name}_{tag}_{platform_sanitized}.json"
            sbom_augmented_file_name = f"{image_name}_{tag}_{platform_sanitized}-augmented.json"
            s3_sbom_lite_path = f"sboms/lite/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}"
            s3_sbom_augmented_path = (
                f"sboms/augmented/{registry}/{organization}/{image_name}/{tag}/{platform_sanitized}"
            )

            create_sbom_light_for_image(image_pull_spec, directory, sbom_lite_file_name, platform)
            upload_to_s3(directory, sbom_lite_file_name, S3_BUCKET, s3_sbom_lite_path)
            upload_sbom_lite_to_silk(directory, sbom_lite_file_name, image_name, platform)
            download_augmented_sbom_from_silk(directory, sbom_augmented_file_name, image_name, platform)
            upload_to_s3(directory, sbom_augmented_file_name, S3_BUCKET, s3_sbom_augmented_path)
    except:
        logger.exception("Skipping SBOM Generation because of an error")

    logger.info(f"Generating SBOM done")
