# This file is the new Sonar
import base64
import sys
import time
from typing import Dict

import boto3
import python_on_whales
from botocore.exceptions import BotoCoreError, ClientError
from python_on_whales.exceptions import DockerException

import docker
from lib.base_logger import logger
from lib.sonar.sonar import create_ecr_repository
from scripts.evergreen.release.images_signing import sign_image, verify_signature


# TODO: self review the PR
def ecr_login_boto3(region: str, account_id: str):
    """
    Fetches an auth token from ECR via boto3 and logs
    into the Docker daemon via the Docker SDK.
    """
    registry = f"{account_id}.dkr.ecr.{region}.amazonaws.com"
    # 1) get token
    ecr = boto3.client("ecr", region_name=region)
    try:
        resp = ecr.get_authorization_token(registryIds=[account_id])
    except (BotoCoreError, ClientError) as e:
        raise RuntimeError(f"Failed to fetch ECR token: {e}")

    auth_data = resp["authorizationData"][0]
    token = auth_data["authorizationToken"]  # base64 of "AWS:password"
    username, password = base64.b64decode(token).decode().split(":", 1)

    # 2) docker login
    client = docker.APIClient()  # low-level client supports login()
    login_resp = client.login(username=username, password=password, registry=registry, reauth=True)
    # login_resp is a dict like {'Status': 'Login Succeeded'}
    status = login_resp.get("Status", "")
    if "Succeeded" not in status:
        raise RuntimeError(f"Docker login failed: {login_resp}")
    logger.debug(f"ECR login succeeded: {status}")


# TODO: don't do it every time ? Check for existence without relying on Exception
def ensure_buildx_builder(builder_name: str = "multiarch") -> str:
    """
    Ensures a Docker Buildx builder exists for multi-platform builds.

    :param builder_name: Name for the buildx builder
    :return: The builder name that was created or reused
    """
    docker = python_on_whales.docker

    try:
        docker.buildx.create(
            name=builder_name,
            driver="docker-container",
            use=True,
            bootstrap=True,
        )
        logger.info(f"Created new buildx builder: {builder_name}")
    except DockerException as e:
        if f'existing instance for "{builder_name}"' in str(e):
            logger.info(f"Builder '{builder_name}' already exists – reusing it.")
            # Make sure it's the current one:
            docker.buildx.use(builder_name)
        else:
            # Some other failure happened
            logger.error(f"Failed to create buildx builder: {e}")
            raise

    return builder_name


def build_image(
    tag: str, dockerfile: str, path: str, args: Dict[str, str] = {}, push: bool = True, platforms: list[str] = None
):
    """
    Build a Docker image using python_on_whales and Docker Buildx for multi-architecture support.

    :param tag: Image tag (name:tag)
    :param dockerfile: Name or relative path of the Dockerfile within `path`
    :param path: Build context path (directory with your Dockerfile)
    :param args: Build arguments dictionary
    :param push: Whether to push the image after building
    :param platforms: List of target platforms (e.g., ["linux/amd64", "linux/arm64"])
    """
    docker = python_on_whales.docker

    try:
        # Convert build args to the format expected by python_on_whales
        build_args = {k: str(v) for k, v in args.items()} if args else {}

        # Set default platforms if not specified
        if platforms is None:
            platforms = ["linux/amd64"]

        logger.info(f"Building image: {tag}")
        logger.info(f"Platforms: {platforms}")
        logger.info(f"Dockerfile: {dockerfile}")
        logger.info(f"Build context: {path}")
        logger.debug(f"Build args: {build_args}")

        # Use buildx for multi-platform builds
        if len(platforms) > 1:
            logger.info(f"Multi-platform build for {len(platforms)} architectures")

        # We need a special driver to handle multi platform builds
        builder_name = ensure_buildx_builder("multiarch")

        # Build the image using buildx
        docker.buildx.build(
            context_path=path,
            file=dockerfile,
            tags=[tag],
            platforms=platforms,
            builder=builder_name,
            build_args=build_args,
            push=push,
            provenance=False,  # To not get an untagged image for single platform builds
            pull=False,  # Don't always pull base images
        )

        logger.info(f"Successfully built {'and pushed' if push else ''} {tag}")

    except Exception as e:
        logger.error(f"Failed to build image {tag}: {e}")
        raise RuntimeError(f"Failed to build image {tag}: {str(e)}")


def process_image(
    image_name: str,
    image_tag: str,
    dockerfile_path: str,
    dockerfile_args: Dict[str, str],
    base_registry: str,
    platforms: list[str] = None,
    sign: bool = False,
    build_path: str = ".",
    push: bool = True,
):
    # Login to ECR using boto3
    ecr_login_boto3(region="us-east-1", account_id="268558157000")  # TODO: use environment variables

    # Helper to automatically create registry with correct name
    should_create_repo = False
    if should_create_repo:
        repo_to_create = "julienben/staging-temp/" + image_name
        logger.debug(f"repo_to_create: {repo_to_create}")
        create_ecr_repository(repo_to_create)
        logger.info(f"Created repository {repo_to_create}")

    # Set default platforms if none provided TODO: remove from here and do it at higher level later
    if platforms is None:
        platforms = ["linux/amd64"]

    docker_registry = f"{base_registry}/{image_name}"
    image_full_uri = f"{docker_registry}:{image_tag}"

    # Build image with docker buildx
    build_image(
        tag=image_full_uri,
        dockerfile=dockerfile_path,
        path=build_path,
        args=dockerfile_args,
        push=push,
        platforms=platforms,
    )

    if sign:
        logger.info("Signing image")
        sign_image(docker_registry, image_tag)
        verify_signature(docker_registry, image_tag)
