import random
import shutil
import subprocess
from typing import Dict, Optional

import docker.errors
from opentelemetry import trace

import docker
from lib.base_logger import logger

from . import SonarAPIError

TRACER = trace.get_tracer("evergreen-agent")


def docker_client() -> docker.DockerClient:
    return docker.client.from_env(timeout=60 * 60 * 24)


@TRACER.start_as_current_span("docker_build")
def docker_build(
    path: str,
    dockerfile: str,
    buildargs: Optional[Dict[str, str]] = None,
    labels: Optional[Dict[str, str]] = None,
    platform: Optional[str] = None,
):
    """Builds a docker image."""

    image_name = "sonar-docker-build-{}".format(random.randint(1, 10000))

    logger.info("path: {}".format(path))
    logger.info("dockerfile: {}".format(dockerfile))
    logger.info("tag: {}".format(image_name))
    logger.info("buildargs: {}".format(buildargs))
    logger.info("labels: {}".format(labels))

    try:
        # docker build from docker-py has bugs resulting in errors or invalid platform when building with specified --platform=linux/amd64 on M1
        docker_build_cli(
            path=path,
            dockerfile=dockerfile,
            tag=image_name,
            buildargs=buildargs,
            labels=labels,
            platform=platform,
        )

        client = docker_client()
        image = client.images.get(image_name)
        logger.info("successfully built docker-image, SHA256: {}".format(image.id))

        span = trace.get_current_span()
        span.set_attribute("mck.image.sha256", image.id)

        return image
    except docker.errors.APIError as e:
        raise SonarAPIError from e


def _get_build_log(e: docker.errors.BuildError) -> str:
    build_logs = "\n"
    for item in e.build_log:
        if "stream" not in item:
            continue
        item_str = item["stream"]
        build_logs += item_str
    return build_logs


def docker_build_cli(
    path: str,
    dockerfile: str,
    tag: str,
    buildargs: Optional[Dict[str, str]],
    labels=Optional[Dict[str, str]],
    platform=Optional[str],
):
    dockerfile_path = dockerfile
    # if dockerfile is relative it has to be set as relative to context (path)
    if not dockerfile_path.startswith("/"):
        dockerfile_path = f"{path}/{dockerfile_path}"

    args = get_docker_build_cli_args(
        path=path, dockerfile=dockerfile_path, tag=tag, buildargs=buildargs, labels=labels, platform=platform
    )

    args_str = " ".join(args)
    logger.info(f"executing cli docker build: {args_str}")

    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if cp.returncode != 0:
        raise SonarAPIError(cp.stderr)


def get_docker_build_cli_args(
    path: str,
    dockerfile: str,
    tag: str,
    buildargs: Optional[Dict[str, str]],
    labels=Optional[Dict[str, str]],
    platform=Optional[str],
):
    # Find docker executable dynamically to work across different environments
    docker_cmd = shutil.which("docker")
    if docker_cmd is None:
        raise Exception("Docker executable not found in PATH")

    args = [docker_cmd, "buildx", "build", "--load", "--progress", "plain", path, "-f", dockerfile, "-t", tag]
    if buildargs is not None:
        for k, v in buildargs.items():
            args.append("--build-arg")
            args.append(f"{k}={v}")

    if labels is not None:
        for k, v in labels.items():
            args.append("--label")
            args.append(f"{k}={v}")

    if platform is not None:
        args.append("--platform")
        args.append(platform)

    return args


def docker_pull(
    image: str,
    tag: str,
):
    client = docker_client()

    try:
        return client.images.pull(image, tag=tag)
    except docker.errors.APIError as e:
        raise SonarAPIError from e


def docker_tag(
    image: docker.models.images.Image,
    registry: str,
    tag: str,
):
    try:
        return image.tag(registry, tag)
    except docker.errors.APIError as e:
        raise SonarAPIError from e


@TRACER.start_as_current_span("image_exists")
def image_exists(repository, tag):
    """Check if a Docker image with the specified tag exists in the repository using efficient HEAD requests."""
    logger.info(f"checking image {tag}, exists in remote repository: {repository}")

    return check_registry_image_exists(repository, tag)


def check_registry_image_exists(repository, tag):
    """Check if image exists in generic registries using HTTP HEAD requests."""
    import requests

    try:
        # Determine registry URL format
        parts = repository.split("/")
        registry_domain = parts[0]
        repository_path = "/".join(parts[1:])

        # Construct URL for manifest check
        url = f"https://{registry_domain}/v2/{repository_path}/manifests/{tag}"
        headers = {"Accept": "application/vnd.docker.distribution.manifest.v2+json"}

        # Make HEAD request instead of full manifest retrieval
        response = requests.head(url, headers=headers, timeout=3)
        return response.status_code == 200
    except Exception as e:
        logger.warning(f"Error checking registry for {repository}:{tag}: {e}")
        return False


@TRACER.start_as_current_span("docker_push")
def docker_push(registry: str, tag: str):
    docker_cmd = shutil.which("docker")
    if docker_cmd is None:
        raise Exception("Docker executable not found in PATH")

    def inner_docker_push(should_raise=False):

        # We can't use docker-py here
        # as it doesn't support DOCKER_CONTENT_TRUST
        # env variable, which could be needed
        cp = subprocess.run(
            [docker_cmd, "push", f"{registry}:{tag}"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        if cp.returncode != 0:
            if should_raise:
                raise SonarAPIError(cp.stderr)

            return False

        return True

    # We don't want to rebuild context images if they already exist.
    # Context images should be out and immutable.
    # This is especially important for base image changes like ubi8 to ubi9, we don't want to replace our existing
    # agent-ubi8 with agent-ubi9 images and break for older operators.
    # Instead of doing the hack here, we should instead either:
    # - make sonar aware of context images
    # - move the logic out of sonar to pipeline.py to all the places where we build context images
    if "-context" in tag and image_exists(registry, tag) and "ecr" not in registry:
        logger.info(f"Image: {tag} in registry: {registry} already exists skipping pushing it")
    else:
        logger.info("Image does not exist remotely or is not a context image, pushing it!")
        retries = 3
        while retries >= 0:
            if inner_docker_push(retries == 0):
                break
            retries -= 1
