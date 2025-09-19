# This file is the new Sonar
import base64
from typing import Dict

import boto3
import docker
import python_on_whales
from botocore.exceptions import BotoCoreError, ClientError
from python_on_whales.exceptions import DockerException

from lib.base_logger import logger

DEFAULT_BUILDER_NAME = "multiarch"  # Default buildx builder name


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


def ensure_buildx_builder(builder_name: str = DEFAULT_BUILDER_NAME) -> str:
    """
    Ensures a Docker Buildx builder exists for multi-platform builds.

    :param builder_name: Name for the buildx builder
    :return: The builder name that was created or reused
    """

    docker_cmd = python_on_whales.docker

    logger.info(f"Ensuring buildx builder '{builder_name}' exists...")
    existing_builders = docker_cmd.buildx.list()
    if any(b.name == builder_name for b in existing_builders):
        logger.info(f"Builder '{builder_name}' already exists – reusing it.")
        docker_cmd.buildx.use(builder_name)
        return builder_name

    try:
        docker_cmd.buildx.create(
            name=builder_name,
            driver="docker-container",
            use=True,
            bootstrap=True,
        )
        logger.info(f"Created new buildx builder: {builder_name}")
    except DockerException as e:
        logger.error(f"Failed to create buildx builder: {e}")
        raise

    return builder_name


def execute_docker_build(
        tags: list[str],
        dockerfile: str,
        path: str, args:
        Dict[str, str],
        push: bool,
        platforms: list[str],
        architecture_suffix: bool = False,
        builder_name: str = DEFAULT_BUILDER_NAME,
):
    """
    Build a Docker image using python_on_whales and Docker Buildx for multi-architecture support.

    :param tags: List of image tags [(name:tag)]
    :param dockerfile: Name or relative path of the Dockerfile within `path`
    :param path: Build context path (directory with the Dockerfile)
    :param args: Build arguments dictionary
    :param push: Whether to push the image after building
    :param platforms: List of target platforms (e.g., ["linux/amd64", "linux/arm64"])
    :param architecture_suffix: Whether to add the architecture of the image as a suffix to the tag
    :param builder_name: Name of the buildx builder to use
    """
    # Login to ECR before building
    # TODO CLOUDP-335471: use env variables to configure AWS region and account ID
    ecr_login_boto3(region="us-east-1", account_id="268558157000")

    docker_cmd = python_on_whales.docker

    try:
        # Convert build args to the format expected by python_on_whales
        build_args = {k: str(v) for k, v in args.items()}

        logger.info(f"Building image: {tags}")
        logger.info(f"Platforms: {platforms}")
        logger.info(f"Dockerfile: {dockerfile}")
        logger.info(f"Build context: {path}")
        logger.debug(f"Build args: {build_args}")

        # Use buildx for multi-platform builds
        if len(platforms) > 1:
            logger.info(f"Multi-platform build for {len(platforms)} architectures")
        elif architecture_suffix and len(platforms) == 1:
            arch = platforms[0].split("/")[1]
            tags = [f"{tag}-{arch}" for tag in tags]
            logger.info(f"Using architecture suffix '{arch}' for tags: {tags}")


        # Build the image using buildx, builder must be already initialized
        docker_cmd.buildx.build(
            context_path=path,
            file=dockerfile,
            tags=tags,
            platforms=platforms,
            builder=builder_name,
            build_args=build_args,
            push=push,
            provenance=False,  # To not get an untagged image for single platform builds
            pull=False,  # Don't always pull base images
        )

        logger.info(f"Successfully built {'and pushed' if push else ''} {tags}")

    except Exception as e:
        logger.error(f"Failed to build image {tags}: {e}")
        raise RuntimeError(f"Failed to build image {tags}: {str(e)}")
