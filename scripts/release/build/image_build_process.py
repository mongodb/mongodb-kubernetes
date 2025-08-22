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


def ensure_ecr_cache_repository(repository_name: str, region: str = "us-east-1"):
    ecr_client = boto3.client("ecr", region_name=region)
    try:
        _ = ecr_client.create_repository(repositoryName=repository_name)
        logger.info(f"Successfully created ECR cache repository: {repository_name}")
    except ClientError as e:
        error_code = e.response['Error']['Code']
        if error_code == 'RepositoryAlreadyExistsException':
            logger.info(f"ECR cache repository already exists: {repository_name}")
        else:
            logger.error(f"Failed to create ECR cache repository {repository_name}: {error_code} - {e}")
            raise


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
        tag: str,
        dockerfile: str,
        path: str,
        args: Dict[str, str],
        push: bool,
        platforms: list[str],
        builder_name: str = DEFAULT_BUILDER_NAME,
):
    """
    Build a Docker image using python_on_whales and Docker Buildx for multi-architecture support.

    :param tag: Image tag (name:tag)
    :param dockerfile: Name or relative path of the Dockerfile within `path`
    :param path: Build context path (directory with the Dockerfile)
    :param args: Build arguments dictionary
    :param push: Whether to push the image after building
    :param platforms: List of target platforms (e.g., ["linux/amd64", "linux/arm64"])
    :param builder_name: Name of the buildx builder to use
    """
    # Login to ECR before building
    # TODO CLOUDP-335471: use env variables to configure AWS region and account ID
    ecr_login_boto3(region="us-east-1", account_id="268558157000")

    docker_cmd = python_on_whales.docker

    try:
        # Convert build args to the format expected by python_on_whales
        build_args = {k: str(v) for k, v in args.items()}

        registry_name = tag.split(":")[0] if ":" in tag else tag
        # e.g., "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes" -> "mongodb-kubernetes"
        cache_image_name = registry_name.split("/")[-1]
        # TODO CLOUDP-335471: use env variables to configure AWS region and account ID

        cache_registry = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/{cache_image_name}"
        cache_from_sources = []
        cache_to_sources = []

        for platform in platforms:
            # Use multiple cache sources for better cache hit rate, and write to all platform-specific caches
            # to avoid race conditions between concurrent multi-arch builds
            arch = platform.split('/')[-1]
            platform_cache = f"{cache_registry}:{arch}"
            cache_from_sources.append(f"type=registry,ref={platform_cache}")
            cache_to_sources = [f"type=registry,ref={cache_registry}:{arch},mode=max,oci-mediatypes=true,image-manifest=true"]

        cache_repo_name = f"dev/cache/{cache_image_name}"
        ensure_ecr_cache_repository(cache_repo_name)

        logger.info(f"Building image: {tag}")
        logger.info(f"Platforms: {platforms}")
        logger.info(f"Dockerfile: {dockerfile}")
        logger.info(f"Build context: {path}")
        logger.info(f"Cache registry: {cache_registry}")
        logger.info(f"Cache from sources: {len(cache_from_sources)} sources")
        logger.info(f"Cache to sources: {len(cache_to_sources)} sources")
        logger.debug(f"Build args: {build_args}")
        logger.debug(f"Cache from: {cache_from_sources}")
        logger.debug(f"Cache to: {cache_to_sources}")

        # Use buildx for multi-platform builds
        if len(platforms) > 1:
            logger.info(f"Multi-platform build for {len(platforms)} architectures")

        # Build the image using buildx, builder must be already initialized
        docker_cmd.buildx.build(
            context_path=path,
            file=dockerfile,
            # TODO: add tag for release builds (OLM immutable tag)
            tags=[tag],
            platforms=platforms,
            builder=builder_name,
            build_args=build_args,
            push=push,
            provenance=False,  # To not get an untagged image for single platform builds
            pull=False,  # Don't always pull base images
            cache_from=cache_from_sources,
            cache_to=cache_to_sources,
        )

        logger.info(f"Successfully built {'and pushed' if push else ''} {tag}")

    except Exception as e:
        logger.error(f"Failed to build image {tag}: {e}")
        raise RuntimeError(f"Failed to build image {tag}: {str(e)}")
