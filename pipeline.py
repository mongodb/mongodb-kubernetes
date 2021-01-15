#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
from datetime import datetime, timedelta
import json
import logging
from typing import Dict, List, Union, Tuple, Optional
import os
import subprocess
import sys
import tarfile

import docker
import requests
from sonar.sonar import process_image

from dataclasses import dataclass, field


LOGLEVEL = os.environ.get("LOGLEVEL", "WARNING").upper()
logging.basicConfig(level=LOGLEVEL)

skippable_tags = frozenset(["ubi", "ubuntu"])

DEFAULT_IMAGE_TYPE = "ubuntu"
DEFAULT_NAMESPACE = "default"


@dataclass
class BuildConfiguration:
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

        args["registry"] = self.base_repository

        return args

    def get_skip_tags(self) -> Optional[Dict[str, str]]:
        return make_list_of_str(self.skip_tags)

    def get_include_tags(self) -> Optional[Dict[str, str]]:
        return make_list_of_str(self.include_tags)


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
    """Builds a running configuration by reading values from environment.
    This is to be used in Evergreen environment.
    """

    # The `base_repo_url` is suffixed with `/dev` because in Evergreen that would
    # replace the `username` we use locally.
    return {
        "image_type": os.environ.get("distro"),
        "base_repo_url": os.environ["registry"] + "/dev",
        "include_tags": os.environ.get("include_tags"),
        "skip_tags": os.environ.get("skip_tags"),
    }


def operator_build_configuration(builder: str, parallel: bool) -> BuildConfiguration:
    default_config_location = os.path.expanduser("~/.operator-dev/context")
    context_file = os.environ.get(
        "OPERATOR_BUILD_CONFIGURATION", default_config_location
    )

    if os.path.exists(context_file):
        context = build_configuration_from_context_file(context_file)
    else:
        context = build_configuration_from_env()

    return BuildConfiguration(
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
    build_configuration: BuildConfiguration,
    args: Dict[str, str] = None,
    inventory="inventory.yaml",
):
    """Calls sonar to build `image_name` with arguments defined in `args`."""
    process_image(
        image_name,
        skip_tags=build_configuration.get_skip_tags(),
        include_tags=build_configuration.get_include_tags(),
        pipeline=build_configuration.pipeline,
        build_args=build_configuration.build_args(args),
        inventory=inventory,
    )


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    image_name = "operator"

    appdb_version = get_release()["appDbBundle"]["mongodbVersion"]  # 4.2.11-ent

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


def build_operator_image_patch(build_configuration: BuildConfiguration):
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
    try:
        image = client.images.get(repo_tag)
    except docker.errors.ImageNotFound:
        print("Operator image does not exist locally. Building it now")
        build_operator_image(build_configuration)
        return

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


def get_supported_version_for_image(image: str) -> List[Dict[str, str]]:
    supported_versions = (
        "https://webhooks.mongodb-realm.com/api/client/v2.0/app/"
        "kubernetes-release-support-kpvbh/service/"
        "supported-{}-versions/incoming_webhook/list".format(image)
    )

    return requests.get(supported_versions).json()


def build_operator_daily(build_configuration: BuildConfiguration):
    """
    Finds all the supported Operator versions and rebuilds them.
    """
    image_name = "operator-daily-build"
    build_id = datetime.now().strftime("%Y%m%d%H%M%S")

    supported_versions = get_supported_version_for_image("operator")
    logging.info("Operator Supported Versions: {}".format(supported_versions))
    for releases in supported_versions:
        logging.info("Rebuilding {}".format(releases["version"]))

        args = dict(build_id=build_id, release_version=releases["version"])
        try:
            sonar_build_image(image_name, build_configuration, args)
        except Exception as e:
            # Log error and continue
            logging.error(e)


def build_init_appdb_daily(build_configuration: BuildConfiguration):
    image_name = "init-appdb-daily"
    build_id = datetime.now().strftime("%Y%m%d%H%M%S")

    supported_versions = get_supported_version_for_image("init-appdb")
    logging.info("Init-AppDB Supported Versions: {}".format(supported_versions))

    for release in supported_versions:
        logging.info("Rebuilding {}".format(release["version"]))

        args = dict(build_id=build_id, release_version=release["version"])
        try:
            sonar_build_image(
                image_name, build_configuration, args, "inventories/init_appdb.yaml"
            )
        except Exception as e:
            # Log error and continue
            logging.error(e)


def build_init_database_daily(build_configuration: BuildConfiguration):
    image_name = "init-database-daily"
    build_id = datetime.now().strftime("%Y%m%d%H%M%S")

    supported_versions = get_supported_version_for_image("init-database")
    logging.info("Init-Database Supported Versions: {}".format(supported_versions))

    for release in supported_versions:
        logging.info("Rebuilding {}".format(release["version"]))

        args = dict(build_id=build_id, release_version=release["version"])
        try:
            sonar_build_image(
                image_name, build_configuration, args, "inventories/init_database.yaml"
            )
        except Exception as e:
            # Log error and continue
            logging.error(e)


def build_init_ops_manager_daily(build_configuration: BuildConfiguration):
    image_name = "init-ops-manager-daily"
    build_id = datetime.now().strftime("%Y%m%d%H%M%S")

    supported_versions = get_supported_version_for_image("init-ops-manager")
    logging.info("Init-Ops-Manager Supported Versions: {}".format(supported_versions))

    for release in supported_versions:
        logging.info("Rebuilding {}".format(release["version"]))

        args = dict(build_id=build_id, release_version=release["version"])
        try:
            sonar_build_image(
                image_name, build_configuration, args, "inventories/init_om.yaml"
            )
        except Exception as e:
            # Log error and continue
            logging.error(e)


def find_om_in_releases(om_version: str, releases: Dict[str, str]) -> Optional[str]:
    """There are a few alternatives out there that allow for json-path or xpath-type
    traversal of Json objects in Python, I don't have time to look for one of
    them now but I have to do at some point.
    """
    for release in releases:
        if release["version"] == om_version:
            for platform in release["platform"]:
                if platform["package_format"] == "deb" and platform["arch"] == "x86_64":
                    for package in platform["packages"]["links"]:
                        if package["name"] == "tar.gz":
                            return package["download_link"]


def get_om_releases() -> Dict[str, str]:
    """Returns a dictionary representation of the Json document holdin all the OM
    releases.
    """
    ops_manager_release_archive = "https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_release_archive.json"

    return requests.get(ops_manager_release_archive).json()


def find_om_url(om_version: str) -> str:
    """Gets a download URL for a given version of OM."""
    releases = get_om_releases()

    current_release = find_om_in_releases(om_version, releases["currentReleases"])
    if current_release is None:
        current_release = find_om_in_releases(om_version, releases["oldReleases"])

    if current_release is None:
        raise ValueError("Ops Manager version {} could not be found".format(om_version))

    return current_release


def build_init_om_image(build_configuration: BuildConfiguration):
    image_name = "init-ops-manager"

    release = get_release()
    init_om_version = release["initOpsManagerVersion"]

    args = dict(version=init_om_version)

    sonar_build_image(image_name, build_configuration, args, "inventories/init_om.yaml")


def build_om_image(build_configuration: BuildConfiguration):
    image_name = "ops-manager"

    # Make this a parameter for the Evergreen build
    # https://github.com/evergreen-ci/evergreen/wiki/Parameterized-Builds
    om_version = os.environ.get("om_version", "4.4.6")
    om_download_url = find_om_url(om_version)
    args = dict(
        om_version=om_version,
        om_download_url=om_download_url,
    )

    sonar_build_image(image_name, build_configuration, args, "inventories/om.yaml")


def build_init_appdb(build_configuration: BuildConfiguration):
    image_name = "init-appdb"

    release = get_release()

    version = release["initAppDbVersion"]
    base_url = "https://fastdl.mongodb.org/tools/db/"

    mongodb_tools_url_ubi = "{}{}".format(
        base_url, release["mongodbToolsBundle"]["ubi"]
    )
    mongodb_tools_url_ubuntu = "{}{}".format(
        base_url, release["mongodbToolsBundle"]["ubuntu"]
    )

    args = dict(
        version=version,
        mongodb_tools_url_ubuntu=mongodb_tools_url_ubuntu,
        mongodb_tools_url_ubi=mongodb_tools_url_ubi,
        is_appdb=True,
    )

    sonar_build_image(
        image_name,
        build_configuration,
        args,
        "inventories/init_appdb.yaml",
    )


def get_builder_function_for_image_name():
    """Returns a dictionary of image names that can be built."""

    return {
        "operator": build_operator_image,
        "operator-quick": build_operator_image_patch,
        #
        # Init images
        "init-appdb": build_init_appdb,
        "init-database": build_init_database,
        "init-ops-manager": build_init_om_image,
        #
        # Daily builds
        "operator-daily": build_operator_daily,
        "init-appdb-daily": build_init_appdb_daily,
        "init-database-daily": build_init_database_daily,
        "init-ops-manager-daily": build_init_ops_manager_daily,
        #
        # Ops Manager image
        "ops-manager": build_om_image,
    }


def build_init_database(build_configuration: BuildConfiguration):
    image_name = "init-database"

    release = get_release()
    version = release["initDatabaseVersion"]  # comes from release.json

    base_url = "https://fastdl.mongodb.org/tools/db/"

    # TODO: Make sure this is required (or not) in the init-database image
    # if not required, remove!
    mongodb_tools_url_ubi = "{}{}".format(
        base_url, release["mongodbToolsBundle"]["ubi"]
    )
    mongodb_tools_url_ubuntu = "{}{}".format(
        base_url, release["mongodbToolsBundle"]["ubuntu"]
    )

    args = dict(
        version=version,
        mongodb_tools_url_ubuntu=mongodb_tools_url_ubuntu,
        mongodb_tools_url_ubi=mongodb_tools_url_ubi,
        is_appdb=False,
    )

    sonar_build_image(
        image_name,
        build_configuration,
        args,
        "inventories/init_database.yaml",
    )


def build_image(image_name: str, build_configuration: BuildConfiguration):
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
