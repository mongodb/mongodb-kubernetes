#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
from datetime import datetime, timedelta
from distutils.dir_util import copy_tree
import json
import logging
from typing import Dict, List, Union, Tuple, Optional
import os
import shutil
import subprocess
import sys
import tarfile

import docker
import requests
from sonar.sonar import process_image

from dataclasses import dataclass, field

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
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
    debug: bool = True

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

    # The `base_repo_url` is suffixed with `/dev` because in Evergreen that
    # would replace the `username` we use locally.
    return {
        "image_type": os.environ.get("distro"),
        "base_repo_url": os.environ["registry"] + "/dev",
        "include_tags": os.environ.get("include_tags"),
        "skip_tags": os.environ.get("skip_tags"),
    }


def operator_build_configuration(
    builder: str, parallel: bool, debug: bool
) -> BuildConfiguration:
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
        debug=debug,
    )


def should_pin_at() -> Optional[Tuple[str, str]]:
    """Gets the value of the pin_tag_at to tag the images with.

    Returns its value splited on :.
    """
    # We need to return something so `partition` does not raise
    # AttributeError
    pinned = os.environ.get("pin_tag_at", "-")
    hour, _, minute = pinned.partition(":")

    return hour, minute


def build_id() -> str:
    """Returns the current UTC time in ISO8601 date format.

    If running in Evergreen and `created_at` expansion is defined, use the
    datetime defined in that variable instead.

    It is possible to pin this time at midnight (00:00) for periodic builds. If
    running a manual build, then the Evergreen `pin_tag_at` variable needs to be
    set to the empty string, in which case, the image tag suffix will correspond
    to the current timestamp.

    """

    date = datetime.utcnow()
    try:
        created_at = os.environ["created_at"]
        date = datetime.strptime(created_at, "%y_%m_%d_%H_%M_%S")
    except KeyError:
        pass

    hour, minute = should_pin_at()
    if hour and minute:
        date = date.replace(hour=int(hour), minute=int(minute), second=0)

    return date.strftime("%Y%m%dT%H%M%SZ")


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def get_git_release_tag() -> str:
    release_env_var = os.getenv("triggered_by_git_tag")

    if release_env_var is not None:
        return release_env_var

    output = subprocess.check_output(
        ["git", "describe", "--tags"],
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
    build_options = {
        # Will continue building an image if it finds an error. See next comment.
        "continue_on_errors": True,
        # But will still fail after all the tasks have completed
        "fail_on_errors": True,
        "pipeline": build_configuration.pipeline,
    }
    process_image(
        image_name,
        skip_tags=build_configuration.get_skip_tags(),
        include_tags=build_configuration.get_include_tags(),
        build_args=build_configuration.build_args(args),
        inventory=inventory,
        build_options=build_options,
    )


def build_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run tests.
    """
    image_name = "test"

    # helm directory needs to be copied over to the tests docker context.
    helm_src = "public/helm_chart"
    helm_dest = "docker/mongodb-enterprise-tests/helm_chart"

    shutil.rmtree(helm_dest, ignore_errors=True)
    copy_tree(helm_src, helm_dest)

    sonar_build_image(image_name, build_configuration, {}, "inventories/test.yaml")


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    image_name = "operator"

    # In evergreen we can pass test_suffix env to publish the operator to a quay
    # repostory with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")

    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")
    args = dict(
        release_version=get_git_release_tag(),
        log_automation_config_diff=log_automation_config_diff,
        test_suffix=test_suffix,
        debug=build_configuration.debug,
    )

    sonar_build_image(image_name, build_configuration, args)


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    image_name = "database"
    release = get_release()

    version = release["databaseImageVersion"]

    args = dict(
        version=version,
    )

    sonar_build_image(
        image_name, build_configuration, args, "inventories/database.yaml"
    )


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
    try:
        client.containers.get(container_name).remove()
        print(f"Removed {container_name}")
    except docker.errors.NotFound:
        pass

    container = client.containers.run(
        repo_tag, name=container_name, entrypoint="sh", detach=True
    )

    print("Building operator with debugging symbols")
    subprocess.run(["make", "manager"], check=True, stdout=subprocess.PIPE)
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

    print("Pushing operator to {}:{}".format(image_repo, image_tag))
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


def image_config(
    image_name: str,
    rh_ospid: str,
    name_prefix: str = "mongodb-enterprise-",
    s3_bucket: str = "enterprise-operator-dockerfiles",
) -> Dict[str, str]:
    """Generates configuration for an image suitable to be passed
    to Sonar.

    It returns a dictionary with registries and S3 configuration."""
    args = {
        "quay_registry": "quay.io/mongodb/{}{}".format(name_prefix, image_name),
        "s3_bucket_http": "https://{}.s3.amazonaws.com/dockerfiles/{}{}".format(
            s3_bucket, name_prefix, image_name
        ),
    }

    if rh_ospid:
        args["rh_registry"] = "scan.connect.redhat.com/ospid-{}/{}{}".format(
            rh_ospid, name_prefix, image_name
        )
    return image_name, args


def args_for_daily_image(image_name: str) -> Dict[str, str]:
    """Returns configuration for an image to be able to be pushed with Sonar.

    This includes the quay_registry and ospid corresponding to RedHat's project id.
    """
    image_configs = [
        image_config("appdb", "31c2f102-af15-4e15-87b9-30710586d9ad"),
        image_config("database", "239de277-d8bb-44b4-8593-73753752317f"),
        image_config(
            "init-appdb",
            "053baed4-c625-44bb-a9bf-a3a5585a17e8",
        ),
        image_config(
            "init-database",
            "cf1063a9-6391-4dd7-b995-a4614483e6a1",
        ),
        image_config(
            "init-ops-manager",
            "7da92b80-396f-4298-9de5-909165ba0c9e",
        ),
        image_config(
            "operator",
            "5558a531-617e-46d7-9320-e84d3458768a",
        ),
        image_config("ops-manager", "b419ca35-17b4-4655-adee-a34e704a6835"),
        image_config(
            "mongodb-agent", "b2beced3-e4db-46e1-9850-4b85ab4ff8d6", name_prefix=""
        ),
    ]

    images = {k: v for k, v in image_configs}
    return images[image_name]


def build_image_daily(image_name: str):
    """Builds a daily image."""

    def inner(build_configuration: BuildConfiguration):
        supported_versions = get_supported_version_for_image(image_name)
        args = args_for_daily_image(image_name)
        args["build_id"] = build_id()
        logging.info(
            "Supported Versions for {}: {}".format(image_name, supported_versions)
        )

        for releases in supported_versions:
            logging.info("Rebuilding {}".format(releases["version"]))
            args["release_version"] = releases["version"]

            try:
                sonar_build_image(
                    "image-daily-build",
                    build_configuration,
                    args,
                    inventory="inventories/daily.yaml",
                )
            except Exception as e:
                # Log error and continue
                logging.error(e)

    return inner


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
    om_version = os.environ.get("om_version")
    if om_version is None:
        raise ValueError("`om_version` should be defined.")

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

    readiness_probe_version = release["readinessProbeVersion"]
    version_upgrade_post_start_hook_version = release[
        "versionUpgradePostStartHookVersion"
    ]

    args = dict(
        version=version,
        mongodb_tools_url_ubuntu=mongodb_tools_url_ubuntu,
        mongodb_tools_url_ubi=mongodb_tools_url_ubi,
        readiness_probe_version=readiness_probe_version,
        version_upgrade_post_start_hook_version=version_upgrade_post_start_hook_version,
        is_appdb=True,
    )

    if os.environ.get("readiness_probe"):
        logging.info(
            "Using readiness_probe source image: %s", os.environ["readiness_probe"]
        )
        repo, tag = os.environ["readiness_probe"].split(":")
        args["readiness_probe_repo"] = repo
        args["readiness_probe_version"] = tag

    sonar_build_image(
        image_name,
        build_configuration,
        args,
        "inventories/init_appdb.yaml",
    )


def get_builder_function_for_image_name():
    """Returns a dictionary of image names that can be built."""

    return {
        "test": build_tests_image,
        "operator": build_operator_image,
        "operator-quick": build_operator_image_patch,
        "database": build_database_image,
        #
        # Init images
        "init-appdb": build_init_appdb,
        "init-database": build_init_database,
        "init-ops-manager": build_init_om_image,
        #
        # Daily builds
        "operator-daily": build_image_daily("operator"),
        "appdb-daily": build_image_daily("appdb"),
        "database-daily": build_image_daily("database"),
        "init-appdb-daily": build_image_daily("init-appdb"),
        "init-database-daily": build_image_daily("init-database"),
        "init-ops-manager-daily": build_image_daily("init-ops-manager"),
        "ops-manager-daily": build_image_daily("ops-manager"),
        "mongodb-agent-daily": build_image_daily("mongodb-agent"),
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

    readiness_probe_version = release["readinessProbeVersion"]
    version_upgrade_post_start_hook_version = release[
        "versionUpgradePostStartHookVersion"
    ]

    args = dict(
        version=version,
        mongodb_tools_url_ubuntu=mongodb_tools_url_ubuntu,
        mongodb_tools_url_ubi=mongodb_tools_url_ubi,
        readiness_probe_version=readiness_probe_version,
        version_upgrade_post_start_hook_version=version_upgrade_post_start_hook_version,
        is_appdb=False,
    )

    # TODO:
    # This is a temporary solution to be able to specify a different readiness_probe image
    # at build time.
    # If this is set to "" or not set at all, then the default value in
    # "inventories/init_database.yaml" will be used.
    if os.environ.get("readiness_probe"):
        logging.info(
            "Using readiness_probe source image: %s", os.environ["readiness_probe"]
        )
        repo, tag = os.environ["readiness_probe"].split(":")
        args["readiness_probe_repo"] = repo
        args["readiness_probe_version"] = tag

    sonar_build_image(
        image_name,
        build_configuration,
        args,
        "inventories/init_database.yaml",
    )


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(
    images: List[str], builder: str, debug: bool = False, parallel: bool = False
):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(builder, parallel, debug)

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
    parser.add_argument("--list-images", action="store_true")
    parser.add_argument("--parallel", action="store_true", default=False)
    parser.add_argument("--debug", action="store_true", default=False)
    args = parser.parse_args()

    if args.list_images:
        print(get_builder_function_for_image_name().keys())
        sys.exit(0)

    images_to_build = calculate_images_to_build(
        get_builder_function_for_image_name().keys(), args.include, args.exclude
    )

    build_all_images(
        images_to_build, args.builder, debug=args.debug, parallel=args.parallel
    )


if __name__ == "__main__":
    main()
