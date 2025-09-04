# This file is the new Sonar
import base64
from typing import Dict, List, Any

import boto3
import docker
import python_on_whales
from botocore.exceptions import BotoCoreError, ClientError
from python_on_whales.exceptions import DockerException

from lib.base_logger import logger
from scripts.release.branch_detection import get_cache_scope, get_current_branch

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


def build_cache_configuration(base_registry: str) -> tuple[list[Any], dict[str, str]]:
    """
    Build cache configuration for branch-scoped BuildKit remote cache.

    Implements the cache strategy:
    - Per-image cache repo: …/dev/cache/<image>
    - Per-branch run with read precedence: branch → master
    - Write to branch scope only
    - Use BuildKit registry cache exporter with mode=max, oci-mediatypes=true, image-manifest=true

    :param base_registry: Base registry URL for cache (e.g., "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/cache/mongodb-kubernetes")
    """
    cache_scope = get_cache_scope()

    # Build cache references with read precedence: branch → master
    cache_from_refs = []

    # Read precedence: branch → master
    branch_ref = f"{base_registry}:{cache_scope}"
    master_ref = f"{base_registry}:master"

    # Add to cache_from in order of precedence
    if cache_scope != "master":
        cache_from_refs.append({"type": "registry", "ref": branch_ref})
        cache_from_refs.append({"type": "registry", "ref": master_ref})
    else:
        cache_from_refs.append({"type": "registry", "ref": master_ref})

    cache_to_refs = {
        "type": "registry",
        "ref": branch_ref,
        "mode": "max",
        "oci-mediatypes": "true",
        "image-manifest": "true"
    }

    return cache_from_refs, cache_to_refs


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
        path: str,
        args: Dict[str, str],
        push: bool,
        platforms: list[str],
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
    :param builder_name: Name of the buildx builder to use
    """
    # Login to ECR before building
    # TODO CLOUDP-335471: use env variables to configure AWS region and account ID
    ecr_login_boto3(region="us-east-1", account_id="268558157000")

    docker_cmd = python_on_whales.docker

    try:
        # Convert build args to the format expected by python_on_whales
        build_args = {k: str(v) for k, v in args.items()}

        cache_from_refs, cache_to_refs = _build_cache(tags)

        logger.info(f"Building image: {tags}")
        logger.info(f"Platforms: {platforms}")
        logger.info(f"Dockerfile: {dockerfile}")
        logger.info(f"Build context: {path}")
        logger.info(f"Cache scope: {get_cache_scope()}")
        logger.info(f"Current branch: {get_current_branch()}")
        logger.info(f"Cache from sources: {len(cache_from_refs)} refs")
        logger.info(f"Cache to targets: {len(cache_to_refs)} refs")
        logger.debug(f"Build args: {build_args}")
        logger.debug(f"Cache from: {cache_from_refs}")
        logger.debug(f"Cache to: {cache_to_refs}")

        # Use buildx for multi-platform builds
        if len(platforms) > 1:
            logger.info(f"Multi-platform build for {len(platforms)} architectures")

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
            cache_from=cache_from_refs,
            cache_to=cache_to_refs,
        )

        logger.info(f"Successfully built {'and pushed' if push else ''} {tags}")

    except Exception as e:
        logger.error(f"Failed to build image {tags}: {e}")
        raise RuntimeError(f"Failed to build image {tags}: {str(e)}")


def _build_cache(tags):
    # Filter tags to only include ECR ones (containing ".dkr.ecr.")
    ecr_tags = [tag for tag in tags if ".dkr.ecr." in tag]
    if not ecr_tags:
        return [], {}
    primary_tag = ecr_tags[0]
    # Extract the repository URL without tag (e.g., "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes:1.0.0" -> "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes")
    repository_url = primary_tag.split(":")[0] if ":" in primary_tag else primary_tag
    # Extract just the image name from the repository URL (e.g., "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes" -> "mongodb-kubernetes")
    cache_image_name = repository_url.split("/")[-1]
    # Base cache repository name
    base_cache_repo = f"dev/cache/{cache_image_name}"
    # Build branch/arch-scoped cache configuration
    base_registry = f"268558157000.dkr.ecr.us-east-1.amazonaws.com/{base_cache_repo}"

    ensure_ecr_cache_repository(base_cache_repo)

    # TODO CLOUDP-335471: use env variables to configure AWS region and account ID
    cache_from_refs, cache_to_refs = build_cache_configuration(
        base_registry
    )
    return cache_from_refs, cache_to_refs
