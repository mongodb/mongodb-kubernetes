import os
import subprocess
import tarfile
from datetime import datetime, timedelta, timezone

import docker


from lib.base_logger import logger
from scripts.release.build_configuration import BuildConfiguration


def copy_into_container(client, src, dst):
    """Copies a local file into a running container."""

    os.chdir(os.path.dirname(src))
    srcname = os.path.basename(src)
    with tarfile.open(src + ".tar", mode="w") as tar:
        tar.add(srcname)

    name, dst = dst.split(":")
    container = client.containers.get(name)

    with open(src + ".tar", "rb") as fd:
        container.put_archive(os.path.dirname(dst), fd.read())


def build_operator_image_fast(build_configuration: BuildConfiguration) -> bool:
    """This function builds the operator locally and pushed into an existing
    Docker image. This is the fastest way I could image we can do this."""

    client = docker.from_env()
    # image that we know is where we build operator.
    image_repo = build_configuration.base_registry + "/" + build_configuration.image_type + "/mongodb-kubernetes"
    image_tag = "latest"
    repo_tag = image_repo + ":" + image_tag

    logger.debug(f"Pulling image: {repo_tag}")
    try:
        image = client.images.get(repo_tag)
    except docker.errors.ImageNotFound:
        logger.debug("Operator image does not exist locally. Building it now")
        return False

    logger.debug("Done")
    too_old = datetime.now() - timedelta(hours=3)
    image_timestamp = datetime.fromtimestamp(
        image.history()[0]["Created"]
    )  # Layer 0 is the latest added layer to this Docker image. [-1] is the FROM layer.

    if image_timestamp < too_old:
        logger.info("Current operator image is too old, will rebuild it completely first")
        return False

    container_name = "mongodb-enterprise-operator"
    operator_binary_location = "/usr/local/bin/mongodb-kubernetes-operator"
    try:
        client.containers.get(container_name).remove()
        logger.debug(f"Removed {container_name}")
    except docker.errors.NotFound:
        pass

    container = client.containers.run(repo_tag, name=container_name, entrypoint="sh", detach=True)

    logger.debug("Building operator with debugging symbols")
    subprocess.run(["make", "manager"], check=True, stdout=subprocess.PIPE)
    logger.debug("Done building the operator")

    copy_into_container(
        client,
        os.getcwd() + "/docker/mongodb-kubernetes-operator/content/mongodb-kubernetes-operator",
        container_name + ":" + operator_binary_location,
    )

    # Commit changes on disk as a tag
    container.commit(
        repository=image_repo,
        tag=image_tag,
    )
    # Stop this container so we can use it next time
    container.stop()
    container.remove()

    logger.info("Pushing operator to {}:{}".format(image_repo, image_tag))
    client.images.push(
        repository=image_repo,
        tag=image_tag,
    )
    return True
