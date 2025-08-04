#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""
import json
import os
import shutil
from concurrent.futures import ProcessPoolExecutor
from copy import copy
from platform import architecture
from queue import Queue
from typing import Callable, Dict, List, Optional, Tuple, Union

import requests
import semver
from opentelemetry import trace
from packaging.version import Version

from lib.base_logger import logger
from scripts.evergreen.release.agent_matrix import (
    get_supported_operator_versions,
)
from scripts.evergreen.release.images_signing import (
    sign_image,
    verify_signature,
)
from scripts.evergreen.release.sbom import generate_sbom, generate_sbom_for_cli

from .build_configuration import BuildConfiguration
from .build_context import BuildScenario
from .build_images import process_image
from .optimized_operator_build import build_operator_image_fast

TRACER = trace.get_tracer("evergreen-agent")
DEFAULT_NAMESPACE = "default"


def make_list_of_str(value: Union[None, str, List[str]]) -> List[str]:
    if value is None:
        return []

    if isinstance(value, str):
        return [e.strip() for e in value.split(",")]

    return value


def get_tools_distro(tools_version: str) -> Dict[str, str]:
    new_rhel_tool_version = "100.10.0"
    default_distro = {"arm": "rhel90-aarch64", "amd": "rhel90-x86_64"}
    if Version(tools_version) >= Version(new_rhel_tool_version):
        return {"arm": "rhel93-aarch64", "amd": "rhel93-x86_64"}
    return default_distro


def is_running_in_evg_pipeline():
    return os.getenv("RUNNING_IN_EVG", "") == "true"


def is_running_in_patch():
    is_patch = os.environ.get("is_patch")
    return is_patch is not None and is_patch.lower() == "true"


def load_release_file() -> Dict:
    with open("release.json") as release:
        return json.load(release)


@TRACER.start_as_current_span("sonar_build_image")
def pipeline_process_image(
    image_name: str,
    dockerfile_path: str,
    build_configuration: BuildConfiguration,
    dockerfile_args: Dict[str, str] = None,
    build_path: str = ".",
    with_sbom: bool = True,
):
    """Builds a Docker image with arguments defined in `args`."""
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)
    if dockerfile_args:
        span.set_attribute("mck.build_args", str(dockerfile_args))

    logger.info(f"Dockerfile args: {dockerfile_args}, for image: {image_name}")

    if not dockerfile_args:
        dockerfile_args = {}
    logger.debug(f"Build args: {dockerfile_args}")
    process_image(
        image_name,
        image_tag=build_configuration.version,
        dockerfile_path=dockerfile_path,
        dockerfile_args=dockerfile_args,
        base_registry=build_configuration.base_registry,
        platforms=build_configuration.platforms,
        sign=build_configuration.sign,
        build_path=build_path,
    )

    if with_sbom:
        produce_sbom(dockerfile_args)


@TRACER.start_as_current_span("produce_sbom")
def produce_sbom(args):
    span = trace.get_current_span()
    if not is_running_in_evg_pipeline():
        logger.info("Skipping SBOM Generation (enabled only for EVG)")
        return

    try:
        image_pull_spec = args["quay_registry"] + args.get("ubi_suffix", "")
    except KeyError:
        logger.error(f"Could not find image pull spec. Args: {args}")
        logger.error(f"Skipping SBOM generation")
        return

    try:
        image_tag = args["release_version"]
        span.set_attribute("mck.release_version", image_tag)
    except KeyError:
        logger.error(f"Could not find image tag. Args: {args}")
        logger.error(f"Skipping SBOM generation")
        return

    image_pull_spec = f"{image_pull_spec}:{image_tag}"
    print(f"Producing SBOM for image: {image_pull_spec} args: {args}")

    platform = "linux/amd64"
    if "platform" in args:
        if args["platform"] == "arm64":
            platform = "linux/arm64"
        elif args["platform"] == "amd64":
            platform = "linux/amd64"
        else:
            raise ValueError(f"Unrecognized platform in {args}. Cannot proceed with SBOM generation")

    generate_sbom(image_pull_spec, platform)


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

    buildargs = dict({"PYTHON_VERSION": python_version})

    pipeline_process_image(
        image_name,
        dockerfile_path="docker/mongodb-kubernetes-tests/Dockerfile",
        build_configuration=build_configuration,
        dockerfile_args=buildargs,
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

    buildargs = dict({"GOLANG_VERSION": golang_version})

    pipeline_process_image(
        image_name,
        dockerfile_path="docker/mongodb-community-tests/Dockerfile",
        build_configuration=build_configuration,
        dockerfile_args=buildargs,
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
        "debug": build_configuration.debug,
    }

    logger.info(f"Building Operator args: {args}")

    image_name = "mongodb-kubernetes"
    build_image_generic(
        image_name=image_name,
        dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_operator_image_patch(build_configuration: BuildConfiguration):
    if not build_operator_image_fast(build_configuration):
        build_operator_image(build_configuration)


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    release = load_release_file()
    version = release["databaseImageVersion"]
    args = {"version": build_configuration.version}
    build_image_generic(
        image_name="mongodb-kubernetes-database",
        dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_CLI_SBOM(build_configuration: BuildConfiguration):
    if not is_running_in_evg_pipeline():
        logger.info("Skipping SBOM Generation (enabled only for EVG)")
        return

    if build_configuration.platforms is None or len(build_configuration.platforms) == 0:
        platforms = ["linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"]
    elif "arm64" in build_configuration.platforms:
        platforms = ["linux/arm64", "darwin/arm64"]
    elif "amd64" in build_configuration.platforms:
        platforms = ["linux/amd64", "darwin/amd64"]
    else:
        logger.error(f"Unrecognized architectures {build_configuration.platforms}. Skipping SBOM generation")
        return

    release = load_release_file()
    version = release["mongodbOperator"]

    for platform in platforms:
        generate_sbom_for_cli(version, platform)


def should_skip_arm64():
    """
    Determines if arm64 builds should be skipped based on environment.
    Returns True if running in Evergreen pipeline as a patch.
    """
    return is_running_in_evg_pipeline() and is_running_in_patch()


@TRACER.start_as_current_span("sign_image_in_repositories")
def sign_image_in_repositories(args: Dict[str, str], arch: str = None):
    span = trace.get_current_span()
    repository = args["quay_registry"] + args["ubi_suffix"]
    tag = args["release_version"]
    if arch:
        tag = f"{tag}-{arch}"

    span.set_attribute("mck.tag", tag)

    sign_image(repository, tag)
    verify_signature(repository, tag)


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
    """Returns a dictionary representation of the Json document holdin all the OM
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
    release = load_release_file()
    version = release["initOpsManagerVersion"]
    args = {"version": build_configuration.version}
    build_image_generic(
        image_name="mongodb-kubernetes-init-ops-manager",
        dockerfile_path="docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
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

    build_image_generic(
        image_name="mongodb-enterprise-ops-manager-ubi",
        dockerfile_path="docker/mongodb-enterprise-ops-manager/Dockerfile",
        build_configuration=build_configuration,
        extra_args=args,
    )


def build_image_generic(
    image_name: str,
    dockerfile_path: str,
    build_configuration: BuildConfiguration,
    extra_args: dict | None = None,
    multi_arch_args_list: list[dict] | None = None,
    is_multi_arch: bool = False,
):
    """
    Build one or more platform-specific images, then (optionally)
    push a manifest and sign the result.
    """

    registry = build_configuration.base_registry
    args_list = multi_arch_args_list or [extra_args or {}]
    version = args_list[0].get("version", "")
    platforms = [args.get("architecture") for args in args_list]

    for base_args in args_list:
        # merge in the registry without mutating caller’s dict
        build_args = {**base_args, "quay_registry": registry}
        logger.debug(f"Build args: {build_args}")

        for arch in platforms:
            logger.debug(f"Building {image_name} for arch={arch}")
            logger.debug(f"build image generic - registry={registry}")
            pipeline_process_image(
                image_name=image_name,
                dockerfile_path=dockerfile_path,
                build_configuration=build_configuration,
                dockerfile_args=build_args,
                with_sbom=False,
            )

    if build_configuration.sign:
        sign_image(registry, version)
        verify_signature(registry, version)


def build_init_appdb(build_configuration: BuildConfiguration):
    release = load_release_file()
    version = release["initAppDbVersion"]
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image_generic(
        image_name="mongodb-kubernetes-init-appdb",
        dockerfile_path="docker/mongodb-kubernetes-init-appdb/Dockerfile",
        build_configuration=build_configuration,
        extra_args=args,
    )


# TODO: nam static: remove this once static containers becomes the default
def build_init_database(build_configuration: BuildConfiguration):
    release = load_release_file()
    version = release["initDatabaseVersion"]  # comes from release.json
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": build_configuration.version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image_generic(
        "mongodb-kubernetes-init-database",
        "docker/mongodb-kubernetes-init-database/Dockerfile",
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
        dockerfile_path = "docker/mongodb-kubernetes-readinessprobe/Dockerfile"
    elif image_type == "upgrade-hook":
        image_name = "mongodb-kubernetes-operator-version-upgrade-post-start-hook"
        dockerfile_path = "docker/mongodb-kubernetes-upgrade-hook/Dockerfile"
    else:
        raise ValueError(f"Unsupported image type: {image_type}")

    version = build_configuration.version
    golang_version = os.getenv("GOLANG_VERSION", "1.24")

    # Use only amd64 if we should skip arm64 builds
    if should_skip_arm64():
        platforms = ["linux/amd64"]
        logger.info("Skipping ARM64 builds for community image as this is running in EVG pipeline as a patch")
    else:
        platforms = build_configuration.platforms or ["linux/amd64", "linux/arm64"]

    # Extract architectures from platforms for build args
    architectures = [platform.split("/")[-1] for platform in platforms]
    multi_arch_args_list = []

    for arch in architectures:
        arch_args = {
            "version": version,
            "GOLANG_VERSION": golang_version,
            "architecture": arch,
            "TARGETARCH": arch,  # TODO: redundant ?
        }
        multi_arch_args_list.append(arch_args)

    # Create a copy of build_configuration with overridden platforms
    build_config_copy = copy(build_configuration)
    build_config_copy.platforms = platforms

    build_image_generic(
        image_name=image_name,
        dockerfile_path=dockerfile_path,
        build_configuration=build_config_copy,
        multi_arch_args_list=multi_arch_args_list,
        is_multi_arch=True,
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
    image_version,
    init_database_image,
    mongodb_tools_url_ubi,
    mongodb_agent_url_ubi: str,
    agent_version,
):
    build_configuration_copy = copy(build_configuration)
    build_configuration_copy.version = image_version
    print(
        f"======== Building agent pipeline for version {image_version}, build configuration version: {build_configuration.version}"
    )
    args = {
        "version": image_version,
        "agent_version": agent_version,
        "ubi_suffix": "-ubi",
        "release_version": image_version,
        "init_database_image": init_database_image,
        "mongodb_tools_url_ubi": mongodb_tools_url_ubi,
        "mongodb_agent_url_ubi": mongodb_agent_url_ubi,
        "quay_registry": build_configuration.base_registry,
    }

    build_image_generic(
        image_name="mongodb-agent-ubi",
        dockerfile_path="docker/mongodb-agent/Dockerfile",
        build_configuration=build_configuration_copy,
        extra_args=args,
    )


def build_multi_arch_agent_in_sonar(
    build_configuration: BuildConfiguration,
    image_version,
    tools_version,
):
    """
    Creates the multi-arch non-operator suffixed version of the agent.
    This is a drop-in replacement for the agent
    release from MCO.
    This should only be called during releases.
    Which will lead to a release of the multi-arch
    images to quay and ecr.
    """

    logger.info(f"building multi-arch base image for: {image_version}")
    args = {
        "version": image_version,
        "tools_version": tools_version,
    }

    arch_arm = {
        "agent_distro": "amzn2_aarch64",
        "tools_distro": get_tools_distro(tools_version=tools_version)["arm"],
        "architecture": "arm64",
    }
    arch_amd = {
        "agent_distro": "rhel9_x86_64",
        "tools_distro": get_tools_distro(tools_version=tools_version)["amd"],
        "architecture": "amd64",
    }

    new_rhel_tool_version = "100.10.0"
    if Version(tools_version) >= Version(new_rhel_tool_version):
        arch_arm["tools_distro"] = "rhel93-aarch64"
        arch_amd["tools_distro"] = "rhel93-x86_64"

    joined_args = [args | arch_amd]

    # Only include arm64 if we shouldn't skip it
    if not should_skip_arm64():
        joined_args.append(args | arch_arm)

    build_image_generic(
        image_name="mongodb-agent-ubi",
        dockerfile_path="docker/mongodb-agent-non-matrix/Dockerfile",
        build_configuration=build_config_copy, #TODO: why ?
        is_multi_arch=True,
        multi_arch_args_list=joined_args,
    )


def build_agent_default_case(build_configuration: BuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    See more information in the function: build_agent_on_agent_bump
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
        logger.info(f"running with factor of {max_workers}")
        print(f"======= Versions to build {agent_versions_to_build} =======")
        for agent_version in agent_versions_to_build:
            # We don't need to keep create and push the same image on every build.
            # It is enough to create and push the non-operator suffixed images only during releases to ecr and quay.
            print(f"======= Building Agent {agent_version} =======")
            _build_agent_operator(
                agent_version,
                build_configuration,
                executor,
                build_configuration.version,
                tasks_queue,
                build_configuration.scenario == BuildScenario.RELEASE,
            )

    queue_exception_handling(tasks_queue)


def build_agent_on_agent_bump(build_configuration: BuildConfiguration):
    """
    Build the agent matrix (operator version x agent version), triggered by PCT.

    We have three cases where we need to build the agent:
    - e2e test runs
    - operator releases
    - OM/CM bumps via PCT

    We don’t require building a full matrix on e2e test runs and operator releases.
    "Operator releases" and "e2e test runs" require only the latest operator x agents

    In OM/CM bumps, we release a new agent which we potentially require to release to older operators as well.
    This function takes care of that.
    """
    release = load_release_file()
    is_release = build_configuration.is_release_step_executed()

    if build_configuration.all_agents:
        # We need to release [all agents x latest operator] on operator releases to make e2e tests work
        # This was changed previously in https://github.com/mongodb/mongodb-kubernetes/pull/3960
        agent_versions_to_build = gather_all_supported_agent_versions(release)
    else:
        # we only need to release the latest images, we don't need to re-push old images, as we don't clean them up anymore.
        agent_versions_to_build = gather_latest_agent_versions(release)

    legacy_agent_versions_to_build = release["supportedImages"]["mongodb-agent"]["versions"]

    tasks_queue = Queue()
    max_workers = 1
    if build_configuration.parallel:
        max_workers = None
        if build_configuration.parallel_factor > 0:
            max_workers = build_configuration.parallel_factor
    with ProcessPoolExecutor(max_workers=max_workers) as executor:
        logger.info(f"running with factor of {max_workers}")

        # We need to regularly push legacy agents, otherwise ecr lifecycle policy will expire them.
        # We only need to push them once in a while to ecr, so no quay required
        if not is_release:
            for legacy_agent in legacy_agent_versions_to_build:
                tasks_queue.put(
                    executor.submit(
                        build_multi_arch_agent_in_sonar,
                        build_configuration,
                        legacy_agent,
                        # we assume that all legacy agents are build using that tools version
                        "100.9.4",
                    )
                )

        for agent_version in agent_versions_to_build:
            # We don't need to keep create and push the same image on every build.
            # It is enough to create and push the non-operator suffixed images only during releases to ecr and quay.
            if build_configuration.is_release_step_executed() or build_configuration.all_agents:
                tasks_queue.put(
                    executor.submit(
                        build_multi_arch_agent_in_sonar,
                        build_configuration,
                        agent_version[0],
                        agent_version[1],
                    )
                )
            for operator_version in get_supported_operator_versions():
                logger.info(f"Building Agent versions: {agent_version} for Operator versions: {operator_version}")
                _build_agent_operator(
                    agent_version, build_configuration, executor, operator_version, tasks_queue, is_release
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
    agent_version: Tuple[str, str],
    build_configuration: BuildConfiguration,
    executor: ProcessPoolExecutor,
    operator_version: str,
    tasks_queue: Queue,
    use_quay: bool = False,
):
    agent_distro = "rhel9_x86_64"
    tools_version = agent_version[1]
    tools_distro = get_tools_distro(tools_version)["amd"]
    image_version = f"{agent_version[0]}_{operator_version}"
    mongodb_tools_url_ubi = (
        f"https://downloads.mongodb.org/tools/db/mongodb-database-tools-{tools_distro}-{tools_version}.tgz"
    )
    mongodb_agent_url_ubi = f"https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-{agent_version[0]}.{agent_distro}.tar.gz"
    init_database_image = f"{build_configuration.base_registry}/mongodb-kubernetes-init-database:{operator_version}"

    tasks_queue.put(
        executor.submit(
            build_agent_pipeline,
            build_configuration,
            image_version,
            init_database_image,
            mongodb_tools_url_ubi,
            mongodb_agent_url_ubi,
            agent_version[0],
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

    # TODO: Remove this once we don't need to use OM 7.0.12 in the OM Multicluster DR tests
    # https://jira.mongodb.org/browse/CLOUDP-297377
    agent_versions_to_build.append(("107.0.12.8669-1", "100.10.0"))

    return sorted(list(set(agent_versions_to_build)))


def get_builder_function_for_image_name() -> Dict[str, Callable]:
    """Returns a dictionary of image names that can be built."""

    image_builders = {
        "cli": build_CLI_SBOM,
        "test": build_tests_image,
        "operator": build_operator_image,
        "mco-test": build_mco_tests_image,
        # TODO: add support to build this per patch
        "readiness-probe": build_readiness_probe_image,
        "upgrade-hook": build_upgrade_hook_image,
        "operator-quick": build_operator_image_patch,
        "database": build_database_image,
        "agent-pct": build_agent_on_agent_bump,
        "agent": build_agent_default_case,
        #
        # Init images
        "init-appdb": build_init_appdb,
        "init-database": build_init_database,
        "init-ops-manager": build_init_om_image,
        #
        # Ops Manager image
        "ops-manager": build_om_image,
    }

    return image_builders
