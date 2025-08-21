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

import python_on_whales
import requests
from opentelemetry import trace

from lib.base_logger import logger
from scripts.release.agent.detect_ops_manager_changes import (
    detect_ops_manager_changes,
    get_all_agents_for_rebuild,
    get_currently_used_agents,
)
from scripts.release.agent.validation import (
    generate_agent_build_args,
    generate_tools_build_args,
    get_available_platforms_for_agent,
    get_available_platforms_for_tools,
)
from scripts.release.build.image_build_configuration import ImageBuildConfiguration
from scripts.release.build.image_build_process import execute_docker_build
from scripts.release.build.image_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)

TRACER = trace.get_tracer("evergreen-agent")


def extract_tools_version_from_release(release: Dict) -> str:
    """
    Extract tools version from release.json mongodbToolsBundle.ubi field.

    Returns:
        Tools version string (e.g., "100.12.2")
    """
    tools_bundle = release["mongodbToolsBundle"]["ubi"]
    # Extract version from filename like "mongodb-database-tools-rhel88-x86_64-100.12.2.tgz"
    # The version is the last part before .tgz
    version_part = tools_bundle.split("-")[-1]  # Gets "100.12.2.tgz"
    tools_version = version_part.replace(".tgz", "")  # Gets "100.12.2"
    return tools_version


def build_image(
    build_configuration: ImageBuildConfiguration,
    build_args: Dict[str, str] = None,
    build_path: str = ".",
):
    """
    Build an image (optionally) sign, then tag and push to all repositories in the registry list.
    """
    image_name = build_configuration.image_name()
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)

    registries = build_configuration.get_registries

    build_args = build_args or {}

    if build_args:
        span.set_attribute("mck.build_args", str(build_args))
    span.set_attribute("mck.registries", str(registries))
    span.set_attribute("mck.platforms", build_configuration.platforms)

    # Build the image once with all repository tags
    all_tags = [f"{registry}:{build_configuration.version}" for registry in build_configuration.registry]

    logger.info(
        f"Building image with tags {all_tags} for platforms={build_configuration.platforms}, dockerfile args: {build_args}"
    )

    execute_docker_build(
        tags=all_tags,
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
        for registry in build_configuration.registry:
            sign_image(registry, build_configuration.version)
            verify_signature(registry, build_configuration.version)


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
    base_url = "https://fastdl.mongodb.org/tools/db"

    tools_version = extract_tools_version_from_release(release)

    if not get_available_platforms_for_tools(tools_version, build_configuration.platforms):
        logger.warning(f"Skipping build for init-appdb - tools version {tools_version} not found in repository")
        return

    platform_build_args = generate_tools_build_args(
        platforms=build_configuration.platforms, tools_version=tools_version
    )

    args = {
        "version": build_configuration.version,
        "mongodb_tools_url": base_url,  # Base URL for platform-specific downloads
        **platform_build_args,  # Add the platform-specific build args
    }

    build_image(
        build_configuration=build_configuration,
        build_args=args,
    )


def build_init_database_image(build_configuration: ImageBuildConfiguration):
    release = load_release_file()
    base_url = "https://fastdl.mongodb.org/tools/db"

    tools_version = extract_tools_version_from_release(release)

    # Validate that the tools version exists before attempting to build
    if not get_available_platforms_for_tools(tools_version, build_configuration.platforms):
        logger.warning(f"Skipping build for init-database - tools version {tools_version} not found in repository")
        return

    platform_build_args = generate_tools_build_args(
        platforms=build_configuration.platforms, tools_version=tools_version
    )

    args = {
        "version": build_configuration.version,
        "mongodb_tools_url": base_url,  # Add the base URL for the Dockerfile
        **platform_build_args,  # Add the platform-specific build args
    }

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


def build_agent(build_configuration: ImageBuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    """
    if build_configuration.all_agents:
        agent_versions_to_build = get_all_agents_for_rebuild()
        logger.info("building all agents")
    elif build_configuration.currently_used_agents:
        agent_versions_to_build = get_currently_used_agents()
        logger.info("building current used agents")
    else:
        agent_versions_to_build = detect_ops_manager_changes()
        logger.info("building agents for changed OM versions")

    if not agent_versions_to_build:
        logger.info("No changes detected, skipping agent build")
        return

    logger.info(f"Building Agent versions: {agent_versions_to_build}")

    tasks_queue = Queue()
    max_workers = 1
    if build_configuration.parallel:
        max_workers = None
        if build_configuration.parallel_factor > 0:
            max_workers = build_configuration.parallel_factor
    with ProcessPoolExecutor(max_workers=max_workers) as executor:
        logger.info(f"Running with factor of {max_workers}")
        logger.info(f"======= Agent versions to build {agent_versions_to_build} =======")

        successful_builds = []
        skipped_builds = []

        for idx, agent_tools_version in enumerate(agent_versions_to_build):
            agent_version = agent_tools_version[0]
            tools_version = agent_tools_version[1]

            available_agent_platforms = get_available_platforms_for_agent(agent_version, build_configuration.platforms)
            available_tools_platforms = get_available_platforms_for_tools(tools_version, build_configuration.platforms)
            available_platforms = list(set(available_agent_platforms) & set(available_tools_platforms))

            logger.info(
                f"======= Building Agent {agent_tools_version} for platforms: {available_platforms}, ({idx + 1}/{len(agent_versions_to_build)})"
            )

            # Check if amd64 is available - if not, skip the entire build
            if "linux/amd64" not in available_platforms:
                logger.warning(
                    f"Skipping agent version {agent_version} - amd64 platform not available (required platform)"
                )
                if available_platforms:
                    logger.info(f"  Other available platforms were: {available_platforms}")
                skipped_builds.append(agent_tools_version)
                continue

            if not available_platforms:
                logger.warning(
                    f"Skipping agent version {agent_version} - no platforms available for both agent and tools"
                )
                skipped_builds.append(agent_tools_version)
                continue

            if available_platforms != build_configuration.platforms:
                logger.info(
                    f"Building agent {agent_version} for available platforms: {available_platforms} "
                    f"(skipping: {set(build_configuration.platforms) - set(available_platforms)})"
                )

            successful_builds.append(agent_tools_version)
            _build_agent(
                agent_tools_version,
                build_configuration,
                available_platforms,
                executor,
                tasks_queue,
            )

    logger.info(f"Build summary: {len(successful_builds)} successful, {len(skipped_builds)} skipped")
    if skipped_builds:
        logger.info(f"Skipped versions: {skipped_builds}")

    queue_exception_handling(tasks_queue)


def _build_agent(
    agent_tools_version: Tuple[str, str],
    build_configuration: ImageBuildConfiguration,
    available_platforms: List[str],
    executor: ProcessPoolExecutor,
    tasks_queue: Queue,
):
    agent_version = agent_tools_version[0]
    tools_version = agent_tools_version[1]

    tasks_queue.put(
        executor.submit(build_agent_pipeline, build_configuration, agent_version, tools_version, available_platforms)
    )


def build_agent_pipeline(
    build_configuration: ImageBuildConfiguration,
    agent_version: str,
    tools_version: str,
    available_platforms: List[str],
):
    build_configuration_copy = copy(build_configuration)
    build_configuration_copy.version = agent_version
    build_configuration_copy.platforms = available_platforms  # Use only available platforms
    print(
        f"======== Building agent pipeline for version {agent_version}, build configuration version: {build_configuration.version}"
    )

    # Note: Validation is now done earlier in the build_agent function
    # Generate platform-specific build arguments using the mapping
    platform_build_args = generate_agent_build_args(
        platforms=available_platforms, agent_version=agent_version, tools_version=tools_version
    )

    agent_base_url = (
        "https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod"
    )
    tools_base_url = "https://fastdl.mongodb.org/tools/db"

    args = {
        "version": agent_version,
        "agent_version": agent_version,
        "mongodb_agent_url": agent_base_url,
        "mongodb_tools_url": tools_base_url,
        **platform_build_args,  # Add the platform-specific build args
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


def load_release_file() -> Dict:
    with open("release.json") as release:
        return json.load(release)
