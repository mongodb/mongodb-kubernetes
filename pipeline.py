#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
from datetime import datetime, timedelta
import json
from typing import Dict, List, Union, Tuple, Optional
import os
import subprocess
import sys
import tarfile

import docker
from sonar.sonar import process_image

from dataclasses import dataclass, field


skippable_tags = frozenset(["ubi", "ubuntu"])

DEFAULT_IMAGE_TYPE = "ubuntu"
DEFAULT_NAMESPACE = "default"


@dataclass
class OperatorBuildConfiguration:
    image_type: str
    base_repository: str
    namespace: str

    include_tags: Optional[List[str]]
    skip_tags: Optional[List[str]]

    builder: str = "docker"
    parallel: bool = False

    pipeline: bool = True

    def build_args(self, args: Optional[Dict[str, str]] = None) -> Dict[str, str]:
        if args is None:
            args = {}
        args = args.copy()

        args["skip_tags"] = make_list_of_str(self.skip_tags)
        args["include_tags"] = make_list_of_str(self.include_tags)

        print("skip_tags:", args["skip_tags"])
        print("include_tags:", args["include_tags"])

        args["registry"] = self.base_repository

        return args


def make_list_of_str(value: Union[None, str, List[str]]) -> List[str]:
    if value is None:
        return []

    if isinstance(value, str):
        return [e.strip() for e in value.split(",")]

    return value


def build_configuration_from_context_file(filename: str) -> Dict[str, str]:
    config = {}
    with open(filename) as fd:
        for line in fd.readlines():
            if line.startswith("#") or line.strip() == "":
                continue

            key, value = line.split("=")
            key = key.replace("export ", "").lower()
            value = value.strip().replace('"', "")
            config[key] = value

    # calculates skip_tags from image_type in local mode
    config["skip_tags"] = list(skippable_tags - {config["image_type"]})

    # explicitely skipping release tags locally
    config["skip_tags"].append("release")

    return config


def build_configuration_from_env() -> Dict[str, str]:
    return {
        "image_type": os.environ.get("distro"),
        "base_repo_url": os.environ["registry"],
        "include_tags": os.environ.get("include_tags"),
        "skip_tags": os.environ.get("skip_tags"),
    }


def operator_build_configuration(
    builder: str, parallel: bool
) -> OperatorBuildConfiguration:
    default_config_location = os.path.expanduser("~/.operator-dev/context")
    context_file = os.environ.get(
        "OPERATOR_BUILD_CONFIGURATION", default_config_location
    )

    if os.path.exists(context_file):
        context = build_configuration_from_context_file(context_file)
    else:
        context = build_configuration_from_env()

    return OperatorBuildConfiguration(
        image_type=context.get("image_type", DEFAULT_IMAGE_TYPE),
        base_repository=context.get("base_repo_url", ""),
        namespace=context.get("namespace", DEFAULT_NAMESPACE),
        skip_tags=context.get("skip_tags"),
        include_tags=context.get("include_tags"),
        builder=builder,
        parallel=parallel,
    )


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def get_git_release_tag() -> str:
    output = subprocess.check_output(
        ["git", "describe"],
    )
    output = output.decode("utf-8")
    return output.strip()


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


def sonar_build_image(
    image_name: str,
    build_configuration: OperatorBuildConfiguration,
    args: Dict[str, str] = None,
):
    """Calls sonar to build `image_name` with arguments defined in `args`."""
    process_image(
        image_name,
        build_configuration.pipeline,
        build_configuration.build_args(args),
    )


def build_operator_image(build_configuration: OperatorBuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    image_name = "operator"

    appdb_version = get_release()["appDbBundle"]["mongodbVersion"]  # 4.2.2-ent

    version = ".".join(appdb_version.split(".")[0:2])
    version_manifest_url = (
        "https://opsmanager.mongodb.com/static/version_manifest/{}.json".format(version)
    )

    # In evergreen we can pass test_suffix env to publish the operator to a quay
    # repostory with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")

    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")
    args = dict(
        version_manifest_url=version_manifest_url,
        mdb_version=appdb_version,
        release_version=get_git_release_tag(),
        log_automation_config_diff=log_automation_config_diff,
        test_suffix=test_suffix,
    )

    sonar_build_image(image_name, build_configuration, args)


def build_operator_image_patch(build_configuration: OperatorBuildConfiguration):
    """This function builds the operator locally and pushed into an existing
    Docker image. This is the fastest way I could image we can do this."""

    client = docker.from_env()
    # image that we know is where we build operator.
    image_repo = (
        build_configuration.base_repository
        + "/"
        + build_configuration.image_type
        + "/mongodb-enterprise-operator"
    )
    image_tag = "latest"
    repo_tag = image_repo + ":" + image_tag

    print("Pulling image:", repo_tag)
    image = client.images.get(repo_tag)
    print("Done")
    too_old = datetime.now() - timedelta(hours=3)
    image_timestamp = datetime.fromtimestamp(
        image.history()[0]["Created"]
    )  # Layer 0 is the latest added layer to this Docker image. [-1] is the FROM layer.

    if image_timestamp < too_old:
        print("Current operator image is too old, will rebuild it completely first")
        build_operator_image(build_configuration)
        return

    container_name = "mongodb-enterprise-operator"
    operator_binary_location = "/usr/local/bin/mongodb-enterprise-operator"
    container = client.containers.run(
        repo_tag, name=container_name, entrypoint="sh", detach=True
    )

    print("Building operator")
    output = subprocess.run(
        "scripts/build/build_operator.sh", check=True, stdout=subprocess.PIPE
    )
    print("Done building the operator")

    copy_into_container(
        client,
        os.getcwd()
        + "/docker/mongodb-enterprise-operator/content/mongodb-enterprise-operator",
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

    print("Pushing operator")
    client.images.push(
        repository=image_repo,
        tag=image_tag,
    )


def get_builder_function_for_image_name():
    """Returns a dictionary of image names that can be built.

    Each one of these functions returns a"""
    return {
        "operator": build_operator_image,
        "operator-quick": build_operator_image_patch,
    }


def build_image(image_name: str, build_configuration: OperatorBuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(images: List[str], builder: str, parallel: bool = False):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(builder, parallel)

    if parallel:
        raise NotImplemented(
            "building images in parallel has not been implemented yet."
        )

    for image in images:
        build_image(image, build_configuration)


def calculate_images_to_build(
    images: List[str], include: Optional[List[str]], exclude: Optional[List[str]]
) -> List[str]:
    """
    Calculates which images to build based on the `images`, `include` and `exclude` sets.

    >>> calculate_images_to_build(["a", "b"], ["a"], ["b"])
    ... ["a"]
    """
    if include is None:
        include = []
    if exclude is None:
        exclude = []

    if len(include) == 0 and len(exclude) == 0:
        return images

    current_images = images

    images_to_build = []
    for image in include:
        if image in current_images:
            images_to_build.append(image)
        else:
            raise ValueError("Image definition {} not found".format(image))

    for image in exclude:
        if image not in images:
            raise ValueError("Image definition {} not found".format(image))

    if len(exclude) > 0:
        for image in current_images:
            if image not in exclude:
                images_to_build.append(image)

    return images_to_build


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--include", action="append")
    parser.add_argument("--exclude", action="append")
    parser.add_argument("--builder", default="docker", type=str)
    parser.add_argument("--list-images")
    parser.add_argument("--parallel", action="store_true", default=False)
    args = parser.parse_args()

    if args.list_images:
        print(get_builder_function_for_image_name().keys())
        sys.exit(0)

    images_to_build = calculate_images_to_build(
        get_builder_function_for_image_name().keys(), args.include, args.exclude
    )

    build_all_images(images_to_build, args.builder, parallel=args.parallel)


if __name__ == "__main__":
    main()
