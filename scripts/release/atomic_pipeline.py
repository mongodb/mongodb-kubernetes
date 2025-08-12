#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""
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
from scripts.evergreen.release.images_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)

from .build_configuration import BuildConfiguration
from .build_context import BuildScenario
from .build_images import execute_docker_build

TRACER = trace.get_tracer("evergreen-agent")


def get_tools_distro(tools_version: str) -> Dict[str, str]:
    new_rhel_tool_version = "100.10.0"
    default_distro = {"arm": "rhel90-aarch64", "amd": "rhel90-x86_64"}
    if Version(tools_version) >= Version(new_rhel_tool_version):
        return {"arm": "rhel93-aarch64", "amd": "rhel93-x86_64"}
    return default_distro


def load_release_file() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def build_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run tests.
    """
    image_name = "mongodb-kubernetes-tests"

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

    python_version = os.getenv("PYTHON_VERSION", "3.11")
    if python_version == "":
        raise Exception("Missing PYTHON_VERSION environment variable")

    buildargs = {"PYTHON_VERSION": python_version}

    build_image(
        image_name=image_name,
        dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
        build_configuration=build_configuration,
        extra_args=buildargs,
        build_path="docker/mongodb-kubernetes-tests",
    )


def build_mco_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run community tests.
    """
    image_name = "mongodb-community-tests"
    golang_version = os.getenv("GOLANG_VERSION", "1.24")
    if golang_version == "":
        raise Exception("Missing GOLANG_VERSION environment variable")

    buildargs = {"GOLANG_VERSION": golang_version}

    build_image(
        image_name=image_name,
        dockerfile_path="docker/mongodb-community-tests/Dockerfile",
        build_configuration=build_configuration,
        extra_args=buildargs,
    )


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    # In evergreen, we can pass test_suffix env to publish the operator to a quay
    # repository with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")
    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")

    args = {
        "version": build_configuration.version,
        "log_automation_config_diff": log_automation_config_diff,
        "test_suffix": test_suffix,
    }

    logger.info(f"Building Operator args: {args}")

    image_name = "mongodb-kubernetes"
    build_image(
        image_name=image_name,
        dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    args = {"version": build_configuration.version}
    build_image(
        image_name="mongodb-kubernetes-database",
        dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
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


def build_init_om_image(build_configuration: BuildConfiguration):
    args = {"version": build_configuration.version}
    build_image(
        image_name="mongodb-kubernetes-init-ops-manager",
        dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_om_image(build_configuration: BuildConfiguration):
    # Make this a parameter for the Evergreen build
    # https://github.com/evergreen-ci/evergreen/wiki/Parameterized-Builds
    om_version = os.environ.get("om_version")
    if om_version is None:
        raise ValueError("`om_version` should be defined.")

    om_download_url = os.environ.get("om_download_url", "")
    if om_download_url == "":
        om_download_url = find_om_url(om_version)

    args = {
        "version": om_version,
        "om_download_url": om_download_url,
    }

    build_image(
        image_name="mongodb-enterprise-ops-manager-ubi",
        dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
    )


@TRACER.start_as_current_span("build_image")
def build_image(
    image_name: str,
    dockerfile_path: str,
    build_configuration: BuildConfiguration,
    extra_args: dict | None = None,
    build_path: str = ".",
):
    """
    Build an image then (optionally) sign the result.
    """
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)

    registry = build_configuration.base_registry
    args_list = extra_args or {}

    # merge in the registry without mutating caller's dict
    build_args = {**args_list, "quay_registry": registry}

    if build_args:
        span.set_attribute("mck.build_args", str(build_args))

    logger.info(f"Building {image_name}, dockerfile args: {build_args}")
    logger.debug(f"Build args: {build_args}")
    logger.debug(f"Building {image_name} for platforms={build_configuration.platforms}")
    logger.debug(f"build image generic - registry={registry}")

    # Build docker registry URI and call build_image
    docker_registry = f"{build_configuration.base_registry}/{image_name}"
    image_full_uri = f"{docker_registry}:{build_configuration.version}"

    execute_docker_build(
        tag=image_full_uri,
        dockerfile=dockerfile_path,
        path=build_path,
        args=build_args,
        push=True,
        platforms=build_configuration.platforms,
    )

    if build_configuration.sign:
        logger.info("Logging in MongoDB Artifactory for Garasign image")
        mongodb_artifactory_login()
        logger.info("Signing image")
        sign_image(docker_registry, build_configuration.version)
        verify_signature(docker_registry, build_configuration.version)


def build_init_appdb(build_configuration: BuildConfiguration):
    release = load_release_file()
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image(
        image_name="mongodb-kubernetes-init-appdb",
        dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
    )


# TODO: nam static: remove this once static containers becomes the default
def build_init_database(build_configuration: BuildConfiguration):
    release = load_release_file()
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image(
        "mongodb-kubernetes-init-database",
        "docker/mongodb-kubernetes-init-database/Dockerfile.atomic",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_community_image(build_configuration: BuildConfiguration, image_type: str):
    """
    Builds image for community components (readiness probe, upgrade hook).

    Args:
        build_configuration: The build configuration to use
        image_type: Type of image to build ("readiness-probe" or "upgrade-hook")
    """

    if image_type == "readiness-probe":
        image_name = "mongodb-kubernetes-readinessprobe"
        dockerfile_path = "docker/mongodb-kubernetes-readinessprobe/Dockerfile.atomic"
    elif image_type == "upgrade-hook":
        image_name = "mongodb-kubernetes-operator-version-upgrade-post-start-hook"
        dockerfile_path = "docker/mongodb-kubernetes-upgrade-hook/Dockerfile.atomic"
    else:
        raise ValueError(f"Unsupported community image type: {image_type}")

    version = build_configuration.version
    golang_version = os.getenv("GOLANG_VERSION", "1.24")

    extra_args = {
        "version": version,
        "GOLANG_VERSION": golang_version,
    }

    build_image(
        image_name=image_name,
        dockerfile_path=dockerfile_path,
        build_configuration=build_configuration,
        extra_args=extra_args,
    )


def build_readiness_probe_image(build_configuration: BuildConfiguration):
    """
    Builds image used for readiness probe.
    """
    build_community_image(build_configuration, "readiness-probe")


def build_upgrade_hook_image(build_configuration: BuildConfiguration):
    """
    Builds image used for version upgrade post-start hook.
    """
    build_community_image(build_configuration, "upgrade-hook")


def build_agent_pipeline(
    build_configuration: BuildConfiguration,
    operator_version: str,
    agent_version: str,
    agent_distro: str,
    tools_version: str,
    tools_distro: str,
):
    image_version = f"{agent_version}_{operator_version}"

    build_configuration_copy = copy(build_configuration)
    build_configuration_copy.version = image_version
    args = {
        "version": image_version,
        "agent_version": agent_version,
        "agent_distro": agent_distro,
        "tools_version": tools_version,
        "tools_distro": tools_distro,
    }

    build_image(
        image_name="mongodb-agent-ubi",
        dockerfile_path="docker/mongodb-agent/Dockerfile.atomic",
        build_configuration=build_configuration_copy,
        extra_args=args,
    )


def build_agent_default_case(build_configuration: BuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    """
    release = load_release_file()

    # We need to release [all agents x latest operator] on operator releases
    if build_configuration.scenario == BuildScenario.RELEASE:
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


def _build_agent_operator(
    agent_tools_version: Tuple[str, str],
    build_configuration: BuildConfiguration,
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
            build_configuration.version,
            agent_version,
            agent_distro,
            tools_version,
            tools_distro,
        )
    )


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
