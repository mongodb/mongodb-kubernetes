#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

# TODO: test pipeline, e.g with a test registry


"""
State of things

All builds are working with reworked Dockerfiles ; except test image
From repo root:

python -m scripts.release.main \
--include upgrade-hook \
--include cli \
--include test \
--include operator \
--include mco-test \
--include readiness-probe \
--include upgrade-hook \
--include operator-quick \
--include database \
--include init-appdb \
--include init-database \
--include init-ops-manager \
--include ops-manager

Should push images to all staging repositories "julienben/staging-temp/***/" on ECR
The base registry is now passed everywhere from one single entry point
Currently hardcoded as TEMP_HARDCODED_BASE_REGISTRY in main.py


Tried to split into smaller files:
- main.py to parse arguments and load image building functions
- build_configuration.py to isolate the dataclass
- build_images.py to replace sonar (basic interactions with Docker)
- optimized_operator_build.py to separate this function which is a mess
- atomic_pipeline.py for everything else

Made a big cleanup (no daily rebuilds, no inventories, no Sonar...) ; still some work to do
The biggest mess is the agent builds

TODO:
- continue to clean pipeline
"""

import json
import os
import shutil
import subprocess
import time
from concurrent.futures import ProcessPoolExecutor
from queue import Queue
from typing import Callable, Dict, Iterable, List, Optional, Tuple, Union

import requests
import semver
from opentelemetry import trace
from packaging.version import Version

import docker
from lib.base_logger import logger
from scripts.evergreen.release.agent_matrix import (
    get_supported_operator_versions,
)
from scripts.evergreen.release.images_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)
from scripts.evergreen.release.sbom import generate_sbom, generate_sbom_for_cli
from .build_configuration import BuildConfiguration

from .build_images import process_image
from .optimized_operator_build import build_operator_image_fast

# TODO: better framework for multi arch builds (spike to come)

TRACER = trace.get_tracer("evergreen-agent")
DEFAULT_IMAGE_TYPE = "ubi"
DEFAULT_NAMESPACE = "default"

# QUAY_REGISTRY_URL sets the base registry for all release build stages. Context images and daily builds will push the
# final images to the registry specified here.
# This makes it easy to use ECR to test changes on the pipeline before pushing to Quay.
QUAY_REGISTRY_URL = "268558157000.dkr.ecr.us-east-1.amazonaws.com/julienben/staging-temp"


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


def operator_build_configuration(
    base_registry: str,
    parallel: bool,
    debug: bool,
    architecture: Optional[List[str]] = None,
    sign: bool = False,
    all_agents: bool = False,
    parallel_factor: int = 0,
) -> BuildConfiguration:
    bc = BuildConfiguration(
        base_registry=base_registry,
        image_type=os.environ.get("distro", DEFAULT_IMAGE_TYPE),
        parallel=parallel,
        all_agents=all_agents or bool(os.environ.get("all_agents", False)),
        debug=debug,
        architecture=architecture,
        sign=sign,
        parallel_factor=parallel_factor,
    )

    logger.info(f"is_running_in_patch: {is_running_in_patch()}")
    logger.info(f"is_running_in_evg_pipeline: {is_running_in_evg_pipeline()}")
    if is_running_in_patch() or not is_running_in_evg_pipeline():
        logger.info(
            f"Running build not in evg pipeline (is_running_in_evg_pipeline={is_running_in_evg_pipeline()}) "
            f"or in pipeline but not from master (is_running_in_patch={is_running_in_patch()}). "
            "Adding 'master' tag to skip to prevent publishing to the latest dev image."
        )

    return bc


def is_running_in_evg_pipeline():
    return os.getenv("RUNNING_IN_EVG", "") == "true"


def is_running_in_patch():
    is_patch = os.environ.get("is_patch")
    return is_patch is not None and is_patch.lower() == "true"


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def get_git_release_tag() -> tuple[str, bool]:
    """Returns the git tag of the current run on releases, on non-release returns the patch id."""
    release_env_var = os.getenv("triggered_by_git_tag")

    # that means we are in a release and only return the git_tag; otherwise we want to return the patch_id
    # appended to ensure the image created is unique and does not interfere
    if release_env_var is not None:
        return release_env_var, True

    patch_id = os.environ.get("version_id", "latest")
    return patch_id, False


def create_and_push_manifest(image: str, tag: str, architectures: list[str]) -> None:
    """
    Generates docker manifests by running the following commands:
    1. Clear existing manifests
    docker manifest rm config.repo_url/image:tag
    2. Create the manifest
    docker manifest create config.repo_url/image:tag --amend config.repo_url/image:tag-amd64 --amend config.repo_url/image:tag-arm64
    3. Push the manifest
    docker manifest push config.repo_url/image:tag

    This method calls docker directly on the command line, this is different from the rest of the code which uses
    Sonar as an interface to docker. We decided to keep this asymmetry for now, as Sonar will be removed soon.
    """
    logger.debug(f"image: {image}, tag: {tag}, architectures: {architectures}")
    final_manifest = image
    logger.debug(f"push_manifest - final_manifest={final_manifest}")

    args = [
        "docker",
        "manifest",
        "create",
        final_manifest,
    ]

    for arch in architectures:
        logger.debug(f"push_manifest - amending {final_manifest}:{tag}-{arch}")
        args.extend(["--amend", f"{final_manifest}:{tag}-{arch}"])

    args_str = " ".join(args)
    logger.debug(f"creating new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)

    args = ["docker", "manifest", "push", final_manifest]
    args_str = " ".join(args)
    logger.info(f"pushing new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)


def try_get_platform_data(client, image):
    """Helper function to try and retrieve platform data."""
    try:
        return client.images.get_registry_data(image)
    except Exception as e:
        logger.error("Failed to get registry data for image: {0}. Error: {1}".format(image, str(e)))
        return None


def check_multi_arch(image: str, suffix: str) -> bool:
    """
    Checks if a docker image supports AMD and ARM platforms by inspecting the registry data.

    :param str image: The image name and tag
    """
    client = docker.from_env()
    platforms = ["linux/amd64", "linux/arm64"]

    for img in [image, image + suffix]:
        reg_data = try_get_platform_data(client, img)
        if reg_data is not None and all(reg_data.has_platform(p) for p in platforms):
            logger.info("Base image {} supports multi architecture, building for ARM64 and AMD64".format(img))
            return True

    logger.info("Base image {} is single-arch, building only for AMD64.".format(img))
    return False


@TRACER.start_as_current_span("sonar_build_image")
def pipeline_process_image(
    image_name: str,
    dockerfile_path: str,
    dockerfile_args: Dict[str, str] = None,
    base_registry: str = None,
    architecture=None,
    sign: bool = False,
    with_sbom: bool = True,
):
    """Builds a Docker image with arguments defined in `args`."""
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)
    if dockerfile_args:
        span.set_attribute("mck.build_args", str(dockerfile_args))

    # TODO use these?
    build_options = {
        # Will continue building an image if it finds an error. See next comment.
        "continue_on_errors": True,
        # But will still fail after all the tasks have completed
        "fail_on_errors": True,
    }

    logger.info(f"Dockerfile args: {dockerfile_args}, for image: {image_name}")

    if not dockerfile_args:
        dockerfile_args = {}
    logger.debug(f"Build args: {dockerfile_args}")
    process_image(
        image_name,
        dockerfile_path=dockerfile_path,
        dockerfile_args=dockerfile_args,
        base_registry=base_registry,
        architecture=architecture,
        sign=sign,
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
            # TODO: return here?
            logger.error(f"Unrecognized architectures in {args}. Skipping SBOM generation")

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

    buildargs = dict({"python_version": python_version})

    # TODO: don't allow test images to be released to Quay
    pipeline_process_image(image_name, "docker/mongodb-kubernetes-tests/Dockerfile", buildargs, base_registry=build_configuration.base_registry)


def build_mco_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run community tests.
    """
    image_name = "community-operator-e2e"
    golang_version = os.getenv("GOLANG_VERSION", "1.24")
    if golang_version == "":
        raise Exception("Missing GOLANG_VERSION environment variable")

    buildargs = dict({"GOLANG_VERSION": golang_version})

    pipeline_process_image(image_name, "docker/mongodb-community-tests/Dockerfile", buildargs, base_registry=build_configuration.base_registry)


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    # In evergreen, we can pass test_suffix env to publish the operator to a quay
    # repository with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")
    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")
    version, _ = get_git_release_tag()

    args = {
        "version": version,
        "log_automation_config_diff": log_automation_config_diff,
        "test_suffix": test_suffix,
        "debug": build_configuration.debug,
    }

    logger.info(f"Building Operator args: {args}")

    image_name = "mongodb-kubernetes"
    build_image_generic(
        image_name=image_name,
        dockerfile_path="docker/mongodb-kubernetes-operator/Dockerfile",
        registry_address=build_configuration.base_registry,
        extra_args=args,
        sign=build_configuration.sign,
    )


def build_operator_image_patch(build_configuration: BuildConfiguration):
    if not build_operator_image_fast(build_configuration):
        build_operator_image(build_configuration)


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    release = get_release()
    version = release["databaseImageVersion"]
    args = {"version": version}
    build_image_generic(image_name="mongodb-kubernetes-database", dockerfile_path="docker/mongodb-kubernetes-database/Dockerfile", registry_address=build_configuration.base_registry, extra_args=args, sign=build_configuration.sign)


def build_CLI_SBOM(build_configuration: BuildConfiguration):
    if not is_running_in_evg_pipeline():
        logger.info("Skipping SBOM Generation (enabled only for EVG)")
        return

    if build_configuration.architecture is None or len(build_configuration.architecture) == 0:
        architectures = ["linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64"]
    elif "arm64" in build_configuration.architecture:
        architectures = ["linux/arm64", "darwin/arm64"]
    elif "amd64" in build_configuration.architecture:
        architectures = ["linux/amd64", "darwin/amd64"]
    else:
        logger.error(f"Unrecognized architectures {build_configuration.architecture}. Skipping SBOM generation")
        return

    release = get_release()
    version = release["mongodbOperator"]

    for architecture in architectures:
        generate_sbom_for_cli(version, architecture)




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
    release = get_release()
    init_om_version = release["initOpsManagerVersion"]
    args = {"version": init_om_version}
    build_image_generic(
        "mongodb-kubernetes-init-ops-manager",
        "docker/mongodb-kubernetes-init-ops-manager/Dockerfile",
        registry_address=build_configuration.base_registry,
        extra_args=args,
        sign=build_configuration.sign,
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
        registry_address=build_configuration.base_registry,
        extra_args=args,
        sign=build_configuration.sign,
    )


def build_image_generic(
    image_name: str,
    dockerfile_path: str,
    registry_address: str,
    extra_args: dict | None = None,
    multi_arch_args_list: list[dict] | None = None,
    is_multi_arch: bool = False,
    sign: bool = False,
):
    """
    Build one or more architecture-specific images, then (optionally)
    push a manifest and sign the result.
    """

    # 1) Defaults
    registry = registry_address
    args_list = multi_arch_args_list or [extra_args or {}]
    version = args_list[0].get("version", "")
    architectures = [args.get("architecture") for args in args_list]

    # 2) Build each arch
    for base_args in args_list:
        # merge in the registry without mutating caller’s dict
        build_args = {**base_args, "quay_registry": registry}
        logger.debug(f"Build args: {build_args}")

        for arch in architectures:
            logger.debug(f"Building {image_name} for arch={arch}")
            logger.debug(f"build image generic - registry={registry}")
            pipeline_process_image(
                image_name,
                dockerfile_path,
                build_args,
                registry,
                architecture=arch,
                sign=False,
                with_sbom=False,
            )

    # 3) Multi-arch manifest
    if is_multi_arch:
        create_and_push_manifest(registry + "/" + image_name, version, architectures=architectures)

    # 4) Signing (only on real releases)
    if sign:
        sign_image(registry, version)
        verify_signature(registry, version)


def build_init_appdb(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initAppDbVersion"]
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "is_appdb": True, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image_generic(
        "mongodb-kubernetes-init-appdb",
        "docker/mongodb-kubernetes-init-appdb/Dockerfile",
        registry_address=build_configuration.base_registry,
        extra_args=args,
        sign=build_configuration.sign,
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

    version, is_release = get_git_release_tag()
    golang_version = os.getenv("GOLANG_VERSION", "1.24")

    # Use only amd64 if we should skip arm64 builds
    if should_skip_arm64():
        architectures = ["amd64"]
        logger.info("Skipping ARM64 builds for community image as this is running in EVG pipeline as a patch")
    else:
        architectures = build_configuration.architecture or ["amd64", "arm64"]

    multi_arch_args_list = []

    for arch in architectures:
        arch_args = {
            "version": version,
            "GOLANG_VERSION": golang_version,
            "architecture": arch,
            "TARGETARCH": arch,
        }
        multi_arch_args_list.append(arch_args)

    build_image_generic(
        image_name=image_name,
        dockerfile_path=dockerfile_path,
        registry_address=build_configuration.base_registry,
        is_multi_arch=True,
        multi_arch_args_list=multi_arch_args_list,
        sign=build_configuration.sign,
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
    args = {
        "version": image_version,
        "mongodb_tools_url_ubi": mongodb_tools_url_ubi,
        "mongodb_agent_url_ubi": mongodb_agent_url_ubi,
        "init_database_image": init_database_image,
    }

    agent_quay_registry = build_configuration.base_registry + f"/mongodb-agent-ubi"
    args["quay_registry"] = build_configuration.base_registry
    args["agent_version"] = agent_version

    build_image_generic(
        image_name="mongodb-agent-ubi",
        dockerfile_path="docker/mongodb-agent/Dockerfile",
        registry_address=build_configuration.base_registry,
        extra_args=args,
        sign=build_configuration.sign,
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
        registry_address=build_configuration.base_registry,
        is_multi_arch=True,
        multi_arch_args_list=joined_args,
        is_run_in_parallel=True,
        sign=build_configuration.sign,
    )


def build_agent_default_case(build_configuration: BuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    See more information in the function: build_agent_on_agent_bump
    """
    release = get_release()

    operator_version, is_release = get_git_release_tag()

    # We need to release [all agents x latest operator] on operator releases
    if is_release:
        agent_versions_to_build = gather_all_supported_agent_versions(release)
    # We only need [latest agents (for each OM major version and for CM) x patch ID] for patches
    else:
        agent_versions_to_build = gather_latest_agent_versions(release)

    logger.info(f"Building Agent versions: {agent_versions_to_build} for Operator versions: {operator_version}")

    tasks_queue = Queue()
    max_workers = 1
    if build_configuration.parallel:
        max_workers = None
        if build_configuration.parallel_factor > 0:
            max_workers = build_configuration.parallel_factor
    with ProcessPoolExecutor(max_workers=max_workers) as executor:
        logger.info(f"running with factor of {max_workers}")
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
            _build_agent_operator(
                agent_version, build_configuration, executor, operator_version, tasks_queue, is_release
            )

    queue_exception_handling(tasks_queue)


def build_agent_on_agent_bump(build_configuration: BuildConfiguration):
    """
    Build the agent matrix (operator version x agent version), triggered by PCT.

    We have three cases where we need to build the agent:
    - e2e test runs
    - operator releases
    - OM/CM bumps via PCT

    We don't require building a full matrix on e2e test runs and operator releases.
    "Operator releases" and "e2e test runs" require only the latest operator x agents

    In OM/CM bumps, we release a new agent which we potentially require to release to older operators as well.
    This function takes care of that.
    """
    release = get_release()
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
    # We use Quay if not in a patch
    # We could rely on input params (quay_registry or registry), but it makes templating more complex in the inventory
    non_quay_registry = os.environ.get("REGISTRY", "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev")
    base_init_database_repo = QUAY_REGISTRY_URL if use_quay else non_quay_registry
    init_database_image = f"{base_init_database_repo}/mongodb-kubernetes-init-database:{operator_version}"

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
    Since we don't want to release all agents again, we only release the latest, which will contain the newly added one
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


# TODO: nam static: remove this once static containers becomes the default
def build_init_database(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initDatabaseVersion"]  # comes from release.json
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi, "is_appdb": False}
    build_image_generic(
        "mongodb-kubernetes-init-database", "docker/mongodb-kubernetes-init-database/Dockerfile", registry_address=build_configuration.base_registry, extra_args=args, sign=build_configuration.sign
    )


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(
    images: Iterable[str],
    base_registry: str,
    debug: bool = False,
    parallel: bool = False,
    architecture: Optional[List[str]] = None,
    sign: bool = False,
    all_agents: bool = False,
    parallel_factor: int = 0,
):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(
        base_registry, parallel, debug, architecture, sign, all_agents, parallel_factor
    )
    if sign:
        mongodb_artifactory_login()
    for idx, image in enumerate(images):
        logger.info(f"====Building image {image} ({idx}/{len(images)-1})====")
        time.sleep(1)
        build_image(image, build_configuration)
