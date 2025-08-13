#!/usr/bin/env python3

"""This atomic_pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters."""
import json
import os
import shutil
from concurrent.futures import ProcessPoolExecutor
from copy import copy
from queue import Queue
from typing import Dict, List, Optional, Tuple

import requests
import semver
from opentelemetry import trace
from packaging.version import Version

from lib.base_logger import logger
from scripts.release.build.image_build_configuration import ImageBuildConfiguration
from scripts.release.build.image_build_process import execute_docker_build
from scripts.release.build.image_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)

TRACER = trace.get_tracer("evergreen-agent")


@TRACER.start_as_current_span("build_image")
def build_image(
    build_configuration: ImageBuildConfiguration,
    build_args: Dict[str, str] = None,
    build_path: str = ".",
):
    """
    Build an image then (optionally) sign the result.
    """
    image_name = build_configuration.image_name()
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)

    base_registry = build_configuration.base_registry()
    build_args = build_args or {}

    if build_args:
        span.set_attribute("mck.build_args", str(build_args))
    span.set_attribute("mck.registry", base_registry)
    span.set_attribute("mck.platforms", build_configuration.platforms)

    # Build docker registry URI and call build_image
    image_full_uri = f"{build_configuration.registry}:{build_configuration.version}"

    logger.info(
        f"Building {image_full_uri} for platforms={build_configuration.platforms}, dockerfile args: {build_args}"
    )

    execute_docker_build(
        tag=image_full_uri,
        dockerfile=build_configuration.dockerfile_path,
        path=build_path,
        args=build_args,
        push=True,
        platforms=build_configuration.platforms,
    )

    if build_configuration.sign:
        logger.info("Logging in MongoDB Artifactory for Garasign image")
        mongodb_artifactory_login()
        logger.info("Signing image")
        sign_image(build_configuration.registry, build_configuration.version)
        verify_signature(build_configuration.registry, build_configuration.version)


def build_meko_tests_image(build_configuration: ImageBuildConfiguration):
    """
    Builds image used to run tests.
    """

    # helm directory needs to be copied over to the tests docker context.
    helm_src = "helm_chart"
    helm_dest = "docker/mongodb-kubernetes-tests/helm_chart"
    requirements_dest = "docker/mongodb-kubernetes-tests/requirements.txt"
    public_src = "public"
    public_dest = "docker/mongodb-kubernetes-tests/public"

    # Remove existing directories/files if they exist
    shutil.rmtree(helm_dest, ignore_errors=True)
    shutil.rmtree(public_dest, ignore_errors=True)

    # Copy directories and files (recursive copy)
    shutil.copytree(helm_src, helm_dest)
    shutil.copytree(public_src, public_dest)
    shutil.copyfile("release.json", "docker/mongodb-kubernetes-tests/release.json")
    shutil.copyfile("requirements.txt", requirements_dest)

    python_version = os.getenv("PYTHON_VERSION", "3.13")
    if python_version == "":
        raise Exception("Missing PYTHON_VERSION environment variable")

    build_args = dict({"PYTHON_VERSION": python_version})

    build_image(
        build_configuration=build_configuration,
        build_args=build_args,
        build_path="docker/mongodb-kubernetes-tests",
    )


def build_mco_tests_image(build_configuration: ImageBuildConfiguration):
    """
    Builds image used to run community tests.
    """

    build_image(
        build_configuration=build_configuration,
    )


def build_operator_image(build_configuration: ImageBuildConfiguration, with_race_detection: bool = False):
    """Calculates arguments required to build the operator image, and starts the build process."""
    # In evergreen, we can pass test_suffix env to publish the operator to a quay
    # repository with a given suffix.
    test_suffix = os.getenv("test_suffix", "")
    log_automation_config_diff = os.getenv("LOG_AUTOMATION_CONFIG_DIFF", "false")

    build_configuration.version = f"{build_configuration.version}{'-race' if with_race_detection else ''}"

    args = {
        "version": build_configuration.version,
        "log_automation_config_diff": log_automation_config_diff,
        "test_suffix": test_suffix,
        "use_race": "true" if with_race_detection else "false",
    }

    logger.info(f"Building Operator args: {args}")

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def build_database_image(build_configuration: ImageBuildConfiguration):
    """
    Builds a new database image.
    """
    args = {"version": build_configuration.version}

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def find_om_in_releases(om_version: str, releases: Dict[str, str]) -> Optional[str]:
    """
    There are a few alternatives out there that allow for json-path or xpath-type
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
    """Returns a dictionary representation of the Json document holding all the OM
    releases.
    """
    ops_manager_release_archive = (
        "https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_release_archive.json"
    )

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


def build_init_om_image(build_configuration: ImageBuildConfiguration):
    args = {"version": build_configuration.version}

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def build_om_image(build_configuration: ImageBuildConfiguration):
    # Make this a parameter for the Evergreen build
    # https://github.com/evergreen-ci/evergreen/wiki/Parameterized-Builds
    om_version = os.environ.get("om_version")
    if om_version is None:
        raise ValueError("`om_version` should be defined.")

    # Set the version in the build configuration (it is not provided in the build_configuration)
    build_configuration.version = om_version

    om_download_url = os.environ.get("om_download_url", "")
    if om_download_url == "":
        om_download_url = find_om_url(om_version)

    args = {
        "version": om_version,
        "om_download_url": om_download_url,
    }

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def build_init_appdb_image(build_configuration: ImageBuildConfiguration):
    release = load_release_file()
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


# TODO: nam static: remove this once static containers becomes the default
def build_init_database_image(build_configuration: ImageBuildConfiguration):
    release = load_release_file()
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def build_readiness_probe_image(build_configuration: ImageBuildConfiguration):
    """
    Builds image used for readiness probe.
    """

    build_image(
        build_configuration=build_configuration,
    )


def build_upgrade_hook_image(build_configuration: ImageBuildConfiguration):
    """
    Builds image used for version upgrade post-start hook.
    """

    build_image(
        build_configuration=build_configuration,
    )


def build_agent_default_case(build_configuration: ImageBuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    """
    release = load_release_file()

    # We need to release [all agents x latest operator] on operator releases
    if build_configuration.is_release_scenario():
        agent_versions_to_build = gather_all_supported_agent_versions(release)
    # We only need [latest agents (for each OM major version and for CM) x patch ID] for patches
    else:
        agent_versions_to_build = gather_latest_agent_versions(release)

    logger.info(
        f"Building Agent versions: {agent_versions_to_build} for Operator versions: {build_configuration.version}"
    )

    tasks_queue = Queue()
    max_workers = 1
    if build_configuration.parallel:
        max_workers = None
        if build_configuration.parallel_factor > 0:
            max_workers = build_configuration.parallel_factor
    with ProcessPoolExecutor(max_workers=max_workers) as executor:
        logger.info(f"Running with factor of {max_workers}")
        logger.info(f"======= Agent versions to build {agent_versions_to_build} =======")
        for idx, agent_tools_version in enumerate(agent_versions_to_build):
            # We don't need to keep create and push the same image on every build.
            # It is enough to create and push the non-operator suffixed images only during releases to ecr and quay.
            logger.info(f"======= Building Agent {agent_tools_version} ({idx}/{len(agent_versions_to_build)})")
            _build_agent_operator(
                agent_tools_version,
                build_configuration,
                executor,
                tasks_queue,
            )

    queue_exception_handling(tasks_queue)


def gather_all_supported_agent_versions(release: Dict) -> List[Tuple[str, str]]:
    # This is a list of a tuples - agent version and corresponding tools version
    agent_versions_to_build = list()
    agent_versions_to_build.append(
        (
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"],
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager_tools"],
        )
    )
    for _, om in release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"].items():
        agent_versions_to_build.append((om["agent_version"], om["tools_version"]))

    # lets not build the same image multiple times
    return sorted(list(set(agent_versions_to_build)))


def gather_latest_agent_versions(release: Dict) -> List[Tuple[str, str]]:
    """
    This function is used when we release a new agent via OM bump.
    That means we will need to release that agent with all supported operators.
    Since we don’t want to release all agents again, we only release the latest, which will contain the newly added one
    :return: the latest agent for each major version
    """
    agent_versions_to_build = list()
    agent_versions_to_build.append(
        (
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager"],
            release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["cloud_manager_tools"],
        )
    )

    latest_versions = {}

    for version in release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"].keys():
        parsed_version = semver.VersionInfo.parse(version)
        major_version = parsed_version.major
        if major_version in latest_versions:
            latest_parsed_version = semver.VersionInfo.parse(str(latest_versions[major_version]))
            latest_versions[major_version] = max(parsed_version, latest_parsed_version)
        else:
            latest_versions[major_version] = version

    for major_version, latest_version in latest_versions.items():
        agent_versions_to_build.append(
            (
                release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][str(latest_version)][
                    "agent_version"
                ],
                release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][str(latest_version)][
                    "tools_version"
                ],
            )
        )

    return sorted(list(set(agent_versions_to_build)))


def _build_agent_operator(
    agent_tools_version: Tuple[str, str],
    build_configuration: ImageBuildConfiguration,
    executor: ProcessPoolExecutor,
    tasks_queue: Queue,
):
    agent_version = agent_tools_version[0]
    agent_distro = "rhel9_x86_64"
    tools_version = agent_tools_version[1]
    tools_distro = get_tools_distro(tools_version)["amd"]

    tasks_queue.put(
        executor.submit(
            build_agent_pipeline,
            build_configuration,
            agent_version,
            agent_distro,
            tools_version,
            tools_distro,
        )
    )


def build_agent_pipeline(
    build_configuration: ImageBuildConfiguration,
    agent_version: str,
    agent_distro: str,
    tools_version: str,
    tools_distro: str,
):
    build_configuration_copy = copy(build_configuration)
    build_configuration_copy.version = agent_version

    print(f"======== Building agent pipeline for version {agent_version}, tools version: {tools_version}")
    args = {
        "version": agent_version,
        "agent_version": agent_version,
        "agent_distro": agent_distro,
        "tools_version": tools_version,
        "tools_distro": tools_distro,
    }

    build_image(
        build_configuration=build_configuration_copy,
        build_args=args,
    )


def queue_exception_handling(tasks_queue):
    exceptions_found = False
    for task in tasks_queue.queue:
        if task.exception() is not None:
            exceptions_found = True
            logger.fatal(f"The following exception has been found when building: {task.exception()}")
    if exceptions_found:
        raise Exception(
            f"Exception(s) found when processing Agent images. \nSee also previous logs for more info\nFailing the build"
        )


def get_tools_distro(tools_version: str) -> Dict[str, str]:
    new_rhel_tool_version = "100.10.0"
    default_distro = {"arm": "rhel90-aarch64", "amd": "rhel90-x86_64"}
    if Version(tools_version) >= Version(new_rhel_tool_version):
        return {"arm": "rhel93-aarch64", "amd": "rhel93-x86_64"}
    return default_distro


def load_release_file() -> Dict:
    with open("release.json") as release:
        return json.load(release)
