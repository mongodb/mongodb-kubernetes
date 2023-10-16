#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
import copy
import json
import logging
import os
import shutil
import subprocess
import sys
import tarfile
from dataclasses import dataclass
from datetime import datetime, timedelta
from distutils.dir_util import copy_tree
from typing import Dict, List, Optional, Tuple, Union

import requests
import semver
from sonar.sonar import process_image

import docker

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
logger = logging.getLogger("pipeline")
logger.setLevel(LOGLEVEL)

skippable_tags = frozenset(["ubi"])

DEFAULT_IMAGE_TYPE = "ubi"
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
    architecture: Optional[List[str]] = None

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


def build_configuration_from_env() -> Dict[str, str]:
    """Builds a running configuration by reading values from environment.
    This is to be used in Evergreen environment.
    """

    # The `base_repo_url` is suffixed with `/dev` because in Evergreen that
    # would replace the `username` we use locally.
    return {
        "image_type": os.environ.get("distro"),
        "base_repo_url": os.environ["BASE_REPO_URL"],
        "include_tags": os.environ.get("include_tags"),
        "skip_tags": os.environ.get("skip_tags"),
    }


def operator_build_configuration(
    builder: str, parallel: bool, debug: bool, architecture: Optional[List[str]] = None
) -> BuildConfiguration:
    context = build_configuration_from_env()

    print(f"Context: {context}")

    return BuildConfiguration(
        image_type=context.get("image_type", DEFAULT_IMAGE_TYPE),
        base_repository=context.get("base_repo_url", ""),
        namespace=context.get("namespace", DEFAULT_NAMESPACE),
        skip_tags=context.get("skip_tags"),
        include_tags=context.get("include_tags"),
        builder=builder,
        parallel=parallel,
        debug=debug,
        architecture=architecture,
    )


class MissingEnvironmentVariable(Exception):
    pass


def should_pin_at() -> Optional[Tuple[str, str]]:
    """Gets the value of the pin_tag_at to tag the images with.

    Returns its value split on :.
    """
    # We need to return something so `partition` does not raise
    # AttributeError
    is_patch = os.environ.get("IS_PATCH", True)

    try:
        pinned = os.environ["pin_tag_at"]
    except KeyError:
        raise MissingEnvironmentVariable(
            f"pin_tag_at environment variable does not exist, but is required"
        )

    if is_patch:
        if pinned == "00:00":
            raise "Pinning to midnight during a patch is not supported. Please pin to another date!"

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
        print(f"we are pinning to, hour: {hour}, minute: {minute}")
        date = date.replace(hour=int(hour), minute=int(minute), second=0)
    else:
        print(
            f"hour and minute cannot be extracted from provided pin_tag_at env, pinning to now"
        )

    string_time = date.strftime("%Y%m%dT%H%M%SZ")

    return string_time


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


"""
Generates docker manifests by running the following commands:
1. Clear existing manifests
docker manifest rm config.repo_url/image:tag
2. Create the manifest
docker manifest create config.repo_url/image:tag --amend config.repo_url/image:tag-amd64 --amend config.repo_url/image:tag-arm64
3. Push the manifest
docker manifest push config.repo_url/image:tag
"""


# This method calls docker directly on the command line, this is different from the rest of the code which uses
# Sonar as an interface to docker. We decided to keep this asymmetry for now, as Sonar will be removed soon.


def create_and_push_manifest(image: str, tag: str) -> None:
    final_manifest = image + ":" + tag
    args = ["docker", "manifest", "rm", final_manifest]
    args_str = " ".join(args)
    print(f"removing existing manifest: {args_str}")
    subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    args = [
        "docker",
        "manifest",
        "create",
        final_manifest,
        "--amend",
        final_manifest + "-amd64",
        "--amend",
        final_manifest + "-arm64",
    ]
    args_str = " ".join(args)
    print(f"creating new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)

    args = ["docker", "manifest", "push", final_manifest]
    args_str = " ".join(args)
    print(f"pushing new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)


"""
Checks if a docker image supports AMD and ARM platforms by inspecting the registry data.

:param str image: The image name and tag
"""


def check_multi_arch(image: str) -> bool:
    client = docker.from_env()

    reg_data = client.images.get_registry_data(image)

    return reg_data.has_platform("linux/amd64") and reg_data.has_platform("linux/arm64")


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

    print(f"Sonar build configuration: {build_configuration}")

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
    helm_src = "helm_chart"
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

    logger.debug("Pulling image:", repo_tag)
    try:
        image = client.images.get(repo_tag)
    except docker.errors.ImageNotFound:
        logger.debug("Operator image does not exist locally. Building it now")
        build_operator_image(build_configuration)
        return

    logger.debug("Done")
    too_old = datetime.now() - timedelta(hours=3)
    image_timestamp = datetime.fromtimestamp(
        image.history()[0]["Created"]
    )  # Layer 0 is the latest added layer to this Docker image. [-1] is the FROM layer.

    if image_timestamp < too_old:
        logger.info(
            "Current operator image is too old, will rebuild it completely first"
        )
        build_operator_image(build_configuration)
        return

    container_name = "mongodb-enterprise-operator"
    operator_binary_location = "/usr/local/bin/mongodb-enterprise-operator"
    try:
        client.containers.get(container_name).remove()
        logger.debug(f"Removed {container_name}")
    except docker.errors.NotFound:
        pass

    container = client.containers.run(
        repo_tag, name=container_name, entrypoint="sh", detach=True
    )

    logger.debug("Building operator with debugging symbols")
    subprocess.run(["make", "manager"], check=True, stdout=subprocess.PIPE)
    logger.debug("Done building the operator")

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

    logger.info("Pushing operator to {}:{}".format(image_repo, image_tag))
    client.images.push(
        repository=image_repo,
        tag=image_tag,
    )


def get_supported_version_for_image(image: str) -> List[Dict[str, str]]:
    return get_release()["supportedImages"][image]["versions"]


def get_supported_variants_for_image(image: str) -> List[Dict[str, str]]:
    return get_release()["supportedImages"][image]["variants"]


def image_config(
    image_name: str,
    name_prefix: str = "mongodb-enterprise-",
    s3_bucket: str = "enterprise-operator-dockerfiles",
    ubi_suffix: str = "-ubi",
) -> Tuple[str, Dict[str, str]]:
    """Generates configuration for an image suitable to be passed
    to Sonar.

    It returns a dictionary with registries and S3 configuration."""
    args = {
        "quay_registry": "quay.io/mongodb/{}{}".format(name_prefix, image_name),
        "ecr_registry_ubi": "268558157000.dkr.ecr.us-east-1.amazonaws.com/images/ubi/{}{}".format(
            name_prefix, image_name
        ),
        "s3_bucket_http": "https://{}.s3.amazonaws.com/dockerfiles/{}{}".format(
            s3_bucket, name_prefix, image_name
        ),
        "ubi_suffix": ubi_suffix,
    }

    return image_name, args


def args_for_daily_image(image_name: str) -> Dict[str, str]:
    """Returns configuration for an image to be able to be pushed with Sonar.

    This includes the quay_registry and ospid corresponding to RedHat's project id.
    """
    image_configs = [
        image_config("appdb"),
        image_config("database"),
        image_config("init-appdb"),
        image_config("init-database"),
        image_config("init-ops-manager"),
        image_config("operator"),
        image_config("ops-manager"),
        image_config("mongodb-agent", name_prefix="", ubi_suffix="-ubi"),
        image_config(
            image_name="mongodb-kubernetes-operator",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
            # community ubi image does not have a suffix in its name
            ubi_suffix="",
        ),
        image_config(
            image_name="mongodb-kubernetes-readinessprobe",
            ubi_suffix="",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
        image_config(
            image_name="mongodb-kubernetes-operator-version-upgrade-post-start-hook",
            ubi_suffix="",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
    ]

    images = {k: v for k, v in image_configs}
    return images[image_name]


"""
Starts the daily build process for an image. This function works for all images we support, for community and 
enterprise operator. The list of supported image_name is defined in get_builder_function_for_image_name.
Builds an image for each version listed in ./release.json
The registry used to pull base image and output the daily build is configured in the image_config function, it is passed
as an argument to the inventories/daily.yaml file.

If the context image supports both ARM and AMD architectures, both will be built.
"""


def build_image_daily(
    image_name: str,
    min_version: str = None,
    max_version: str = None,
):
    """Builds a daily image."""

    def inner(build_configuration: BuildConfiguration):
        supported_versions = get_supported_version_for_image(image_name)
        variants = get_supported_variants_for_image(image_name)

        args = args_for_daily_image(image_name)
        args["build_id"] = build_id()
        logger.info(
            "Supported Versions for {}: {}".format(image_name, supported_versions)
        )
        completed_versions = set()
        for version in supported_versions:
            if (
                min_version is not None
                and max_version is not None
                and (
                    semver.compare(version, min_version) < 0
                    or semver.compare(version, max_version) >= 0
                )
            ):
                continue

            build_configuration = copy.deepcopy(build_configuration)
            if build_configuration.include_tags is None:
                build_configuration.include_tags = []

            build_configuration.include_tags.extend(variants)

            logger.info("Rebuilding {} with variants {}".format(version, variants))
            args["release_version"] = version

            arch_set = set()
            if build_configuration.architecture:
                arch_set = set(build_configuration.architecture)

            if arch_set == {"arm64"}:
                raise ValueError("Building for ARM64 only is not supported yet")

            if version not in completed_versions:
                try:
                    # Automatic architecture detection is the default behavior if 'arch' argument isn't specified
                    if check_multi_arch(
                        args["quay_registry"]
                        + args["ubi_suffix"]
                        + ":"
                        + args["release_version"]
                        + "-"
                        + "context"
                    ) and (arch_set == {"amd64", "arm64"} or arch_set == set()):
                        sonar_build_image(
                            "image-daily-build-amd64",
                            build_configuration,
                            args,
                            inventory="inventories/daily.yaml",
                        )
                        sonar_build_image(
                            "image-daily-build-arm64",
                            build_configuration,
                            args,
                            inventory="inventories/daily.yaml",
                        )
                        create_and_push_manifest(
                            args["quay_registry"], args["release_version"]
                        )
                        create_and_push_manifest(
                            args["quay_registry"],
                            args["release_version"] + "-b" + args["build_id"],
                        )
                        create_and_push_manifest(
                            args["ecr_registry_ubi"], args["release_version"]
                        )
                        create_and_push_manifest(
                            args["ecr_registry_ubi"],
                            args["release_version"] + "-b" + args["build_id"],
                        )
                    else:
                        sonar_build_image(
                            "image-daily-build",
                            build_configuration,
                            args,
                            inventory="inventories/daily.yaml",
                        )
                    completed_versions.add(version)
                except Exception as e:
                    # Log error and continue
                    logger.error(e)

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
    return None


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

    om_download_url = os.environ.get("om_download_url", "")
    if om_download_url == "":
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

    args = dict(
        version=version,
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
        "ops-manager-5-daily": build_image_daily(
            "ops-manager", min_version="5.0.0", max_version="6.0.0"
        ),
        "ops-manager-6-daily": build_image_daily(
            "ops-manager", min_version="6.0.0", max_version="7.0.0"
        ),
        #
        # Ops Manager image
        "ops-manager": build_om_image,
        #
        # Community images
        "mongodb-agent-daily": build_image_daily("mongodb-agent"),
        "mongodb-kubernetes-readinessprobe-daily": build_image_daily(
            "mongodb-kubernetes-readinessprobe",
        ),
        "mongodb-kubernetes-operator-version-upgrade-post-start-hook-daily": build_image_daily(
            "mongodb-kubernetes-operator-version-upgrade-post-start-hook",
        ),
        "mongodb-kubernetes-operator-daily": build_image_daily(
            "mongodb-kubernetes-operator"
        ),
    }


def build_init_database(build_configuration: BuildConfiguration):
    image_name = "init-database"

    release = get_release()
    version = release["initDatabaseVersion"]  # comes from release.json

    base_url = "https://fastdl.mongodb.org/tools/db/"

    mongodb_tools_url_ubi = "{}{}".format(
        base_url, release["mongodbToolsBundle"]["ubi"]
    )

    args = dict(
        version=version,
        mongodb_tools_url_ubi=mongodb_tools_url_ubi,
        is_appdb=False,
    )

    # TODO:
    # This is a temporary solution to be able to specify a different readiness_probe image
    # at build time.
    # If this is set to "" or not set at all, then the default value in
    # "inventories/init_database.yaml" will be used.
    if os.environ.get("readiness_probe"):
        logger.info(
            "Using readiness_probe source image: %s", os.environ["readiness_probe"]
        )
        repo, tag = os.environ["readiness_probe"].split(":")

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
    images: List[str],
    builder: str,
    debug: bool = False,
    parallel: bool = False,
    architecture: Optional[List[str]] = None,
):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(
        builder, parallel, debug, architecture
    )

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
    parser.add_argument(
        "--arch",
        choices=["amd64", "arm64"],
        nargs="+",
        help="for daily builds only, specify the list of architectures to build for images",
    )
    args = parser.parse_args()

    if args.list_images:
        print(get_builder_function_for_image_name().keys())
        sys.exit(0)

    if args.arch == ["arm64"]:
        print("Building for arm64 only is not supported yet")
        sys.exit(1)

    images_to_build = calculate_images_to_build(
        get_builder_function_for_image_name().keys(), args.include, args.exclude
    )

    build_all_images(
        images_to_build,
        args.builder,
        debug=args.debug,
        parallel=args.parallel,
        architecture=args.arch,
    )


if __name__ == "__main__":
    main()
