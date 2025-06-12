# Methods responsible for building and pushing docker images.
import sys
import traceback

import boto3
from botocore.exceptions import BotoCoreError, ClientError
import base64

from lib.base_logger import logger
import docker

logger.info("Starting build images script")

IMAGE_NAME = "mongodb-kubernetes-operator"
DOCKERFILES_PATH = f"./docker/{IMAGE_NAME}"
CONTEXT_DOCKERFILE = "Dockerfile"
RELEASE_DOCKERFILE = "Dockerfile.plain"
STAGING_REGISTRY = "268558157000.dkr.ecr.us-east-1.amazonaws.com/julienben/operator-staging-temp"
LATEST_TAG = "latest"
LATEST_TAG_CONTEXT = f"{LATEST_TAG}-context"

def ecr_login_boto3(region: str, account_id: str):
    """
    Fetches an auth token from ECR via boto3 and logs
    into the Docker daemon via the Docker SDK.
    """
    registry = f"{account_id}.dkr.ecr.{region}.amazonaws.com"
    # 1) get token
    boto3.setup_default_session(profile_name='default')
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
    login_resp = client.login(
        username=username,
        password=password,
        registry=registry,
        reauth=True
    )
    # login_resp is a dict like {'Status': 'Login Succeeded'}
    status = login_resp.get("Status", "")
    if "Succeeded" not in status:
        raise RuntimeError(f"Docker login failed: {login_resp}")
    logger.info(f"ECR login succeeded: {status}")

def build_image(docker_client: docker.DockerClient, tag: str, dockerfile: str, path: str, args=None):
    """
    Build a Docker image.

    :param path: Build context path (directory with your Dockerfile)
    :param dockerfile: Name or relative path of the Dockerfile within `path`
    :param tag: Image tag (name:tag)
    """

    try:
        image, logs = docker_client.images.build(
            path=path,
            dockerfile=dockerfile,
            tag=tag,
            rm=True,        # remove intermediate containers after a successful build
            pull=False,     # set True to always attempt to pull a newer base image
            buildargs=args  # pass build args if provided
        )
        logger.info(f"Successfully built {tag} (id: {image.id})")
        # Print build output
        for chunk in logs:
            if 'stream' in chunk:
                logger.debug(chunk['stream'])
    except docker.errors.BuildError as e:
        logger.error("Build failed:")
        for stage in e.build_log:
            if "stream" in stage:
                logger.debug(stage["stream"])
            elif "error" in stage:
                logger.error(stage["error"])
        logger.error(e)
        sys.exit(1)
    except Exception as e:
        logger.error(f"Unexpected error: {e}")
        sys.exit(2)

def push_image(docker_client: docker.DockerClient, image: str, tag: str):
    """
    Push a Docker image to a registry.

    :param image: Image name (e.g., 'my-image')
    :param tag: Image tag (e.g., 'latest')
    """
    try:
        response = docker_client.images.push(image, tag=tag)
        logger.info(f"Successfully pushed {image}:{tag}")
        logger.debug(response)
    except docker.errors.APIError as e:
        logger.error(f"Failed to push image {image}:{tag} - {e}")
        sys.exit(1)

if __name__ == '__main__':
    docker_client = docker.from_env()
    logger.info("Docker client initialized")

    # Login to ECR using boto3
    ecr_login_boto3(region='us-east-1', account_id='268558157000')

    # Build context image
    image_full_tag = f"{STAGING_REGISTRY}:{LATEST_TAG_CONTEXT}"
    logger.info(f"Building image: {image_full_tag}")
    context_dockerfile_full_path = f"{DOCKERFILES_PATH}/{CONTEXT_DOCKERFILE}"
    logger.info(f"Using Dockerfile at: {context_dockerfile_full_path}")
    build_image(docker_client, path=".", dockerfile=context_dockerfile_full_path, tag=LATEST_TAG_CONTEXT, args={'version': '0.0.1'})

    # Push to staging registry
    push_image(docker_client, STAGING_REGISTRY, LATEST_TAG_CONTEXT)

    # Build release image
    release_image_full_tag = f'{STAGING_REGISTRY}:latest'
    release_dockerfile_full_path = f"{DOCKERFILES_PATH}/{RELEASE_DOCKERFILE}"
    logger.info(f"Building release image with tag: {release_image_full_tag}")
    logger.info(f"Using Dockerfile at: {release_dockerfile_full_path}")

    build_image(docker_client, path=".", dockerfile=release_dockerfile_full_path, tag=release_image_full_tag, args={'imagebase': image_full_tag})

    # Push release image
    push_image(docker_client, STAGING_REGISTRY, LATEST_TAG)
