# This file is the new Sonar
import base64
import sys
from typing import Dict

import boto3
from botocore.exceptions import BotoCoreError, ClientError

import docker
from lib.base_logger import logger
from lib.sonar.sonar import create_ecr_repository
from scripts.evergreen.release.images_signing import sign_image, verify_signature


# TODO use either from python_on_whales import docker to use buildx and build multi arch image at once
#  or subprocess with cmd = [ "docker", "buildx", "build", "--platform", platforms ]
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


def build_image(docker_client: docker.DockerClient, tag: str, dockerfile: str, path: str, args: Dict[str, str] = {}):
    """
    Build a Docker image.

    :param docker_client:
    :param path: Build context path (directory with your Dockerfile)
    :param dockerfile: Name or relative path of the Dockerfile within `path`
    :param tag: Image tag (name:tag)
    :param args:
    """

    try:
        if args:
            args = {k: str(v) for k, v in args.items()}
        image, logs = docker_client.images.build(
            path=path,
            dockerfile=dockerfile,
            tag=tag,
            pull=False,  # set True to always attempt to pull a newer base image
            buildargs=args,
        )
        logger.info(f"Successfully built {tag} (id: {image.id})")
        # Print build output
        for chunk in logs:
            if "stream" in chunk:
                logger.debug(chunk["stream"])
    except docker.errors.BuildError as e:
        logger.error("Build failed:")
        for stage in e.build_log:
            if "stream" in stage:
                logger.debug(stage["stream"])
            elif "error" in stage:
                logger.error(stage["error"])
        logger.error(e)
        logger.error(
            "Note that the docker client only surfaces the general error message. For detailed troubleshooting of the build failure, run the equivalent build command locally or use the docker Python API client directly."
        )
        raise RuntimeError(f"Failed to build image {tag}")
    except Exception as e:
        logger.error(f"Unexpected error: {e}")
        raise RuntimeError(f"Failed to build image {tag}")


def push_image(docker_client: docker.DockerClient, image: str, tag: str):
    """
    Push a Docker image to a registry.

    :param docker_client:
    :param image: Image name (e.g., 'my-image')
    :param tag: Image tag (e.g., 'latest')
    """
    logger.debug(f"push_image - image: {image}, tag: {tag}")
    image_full_uri = f"{image}:{tag}"
    try:
        output = docker_client.images.push(image, tag=tag)
        if "error" in output:
            raise RuntimeError(f"Failed to push image {image_full_uri} {output}")
        logger.info(f"Successfully pushed {image_full_uri}")
    except Exception as e:
        logger.error(f"Failed to push image {image_full_uri} - {e}")
        sys.exit(1)


def process_image(
    image_name: str,
    image_tag: str,
    dockerfile_path: str,
    dockerfile_args: Dict[str, str],
    base_registry: str,
    architecture: str = None,
    sign: bool = False,
    build_path: str = ".",
):
    docker_client = docker.from_env()
    logger.debug("Docker client initialized")
    # Login to ECR using boto3
    ecr_login_boto3(region="us-east-1", account_id="268558157000")

    # Helper to automatically create registry with correct name
    should_create_repo = False
    if should_create_repo:
        repo_to_create = "julienben/staging-temp/" + image_name
        logger.debug(f"repo_to_create: {repo_to_create}")
        create_ecr_repository(repo_to_create)
        logger.info(f"Created repository {repo_to_create}")

    # Build image
    docker_registry = f"{base_registry}/{image_name}"
    arch_tag = f"-{architecture}" if architecture else ""
    image_tag = f"{image_tag}{arch_tag}"
    image_full_uri = f"{docker_registry}:{image_tag}"
    logger.info(f"Building image: {image_full_uri}")
    logger.info(f"Using Dockerfile at: {dockerfile_path}, and build path: {build_path}")
    logger.debug(f"Build args: {dockerfile_args}")
    build_image(
        docker_client, path=build_path, dockerfile=f"{dockerfile_path}", tag=image_full_uri, args=dockerfile_args
    )

    # Push to staging registry
    logger.info(f"Pushing image: {image_tag} to {docker_registry}")
    push_image(docker_client, docker_registry, image_tag)

    if sign:
        logger.info("Signing image")
        sign_image(docker_registry, image_tag)
        verify_signature(docker_registry, image_tag)
