#!/usr/bin/env python3

"""This pipeline script knows about the details of our Docker images
and where to fetch and calculate parameters. It uses Sonar.py
to produce the final images."""

import argparse
import copy
import json
import os
import random
import shutil
import subprocess
import sys
import tarfile
import time
import traceback
from concurrent.futures import ProcessPoolExecutor, ThreadPoolExecutor
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from queue import Queue
from typing import Callable, Dict, Iterable, List, Optional, Set, Tuple, Union

import requests
import semver
from opentelemetry import context
from opentelemetry import context as otel_context
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import (
    OTLPSpanExporter as OTLPSpanGrpcExporter,
)
from opentelemetry.sdk.resources import SERVICE_NAME, Resource
from opentelemetry.sdk.trace import (
    SynchronousMultiSpanProcessor,
    Tracer,
    TracerProvider,
)
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import NonRecordingSpan, SpanContext, TraceFlags
from packaging.version import Version

import docker
from lib.base_logger import logger
from lib.sonar.sonar import process_image
from scripts.evergreen.release.agent_matrix import (
    get_supported_operator_versions,
    get_supported_version_for_image_matrix_handling,
)
from scripts.evergreen.release.images_signing import (
    mongodb_artifactory_login,
    sign_image,
    verify_signature,
)
from scripts.evergreen.release.sbom import generate_sbom, generate_sbom_for_cli

TRACER = trace.get_tracer("evergreen-agent")


def _setup_tracing():
    trace_id = os.environ.get("otel_trace_id")
    parent_id = os.environ.get("otel_parent_id")
    endpoint = os.environ.get("otel_collector_endpoint")
    if any(value is None for value in [trace_id, parent_id, endpoint]):
        logger.info("tracing environment variables are missing, not configuring tracing")
        return
    logger.info(f"parent_id is {parent_id}")
    logger.info(f"trace_id is {trace_id}")
    logger.info(f"endpoint is {endpoint}")
    span_context = SpanContext(
        trace_id=int(trace_id, 16),
        span_id=int(parent_id, 16),
        is_remote=False,
        # This flag ensures the span is sampled and sent to the collector
        trace_flags=TraceFlags(0x01),
    )
    ctx = trace.set_span_in_context(NonRecordingSpan(span_context))
    context.attach(ctx)
    sp = SynchronousMultiSpanProcessor()
    span_processor = BatchSpanProcessor(
        OTLPSpanGrpcExporter(
            endpoint=endpoint,
        )
    )
    sp.add_span_processor(span_processor)
    resource = Resource(attributes={SERVICE_NAME: "evergreen-agent"})
    provider = TracerProvider(resource=resource, active_span_processor=sp)
    trace.set_tracer_provider(provider)


DEFAULT_IMAGE_TYPE = "ubi"
DEFAULT_NAMESPACE = "default"

# QUAY_REGISTRY_URL sets the base registry for all release build stages. Context images and daily builds will push the
# final images to the registry specified here.
# This makes it easy to use ECR to test changes on the pipeline before pushing to Quay.
QUAY_REGISTRY_URL = os.environ.get("QUAY_REGISTRY", "quay.io/mongodb")


@dataclass
class BuildConfiguration:
    image_type: str
    base_repository: str
    namespace: str

    include_tags: list[str]
    skip_tags: list[str]

    builder: str = "docker"
    parallel: bool = False
    parallel_factor: int = 0
    architecture: Optional[List[str]] = None
    sign: bool = False
    all_agents: bool = False
    agent_to_build: str = ""

    pipeline: bool = True
    debug: bool = True

    def build_args(self, args: Optional[Dict[str, str]] = None) -> Dict[str, str]:
        if args is None:
            args = {}
        args = args.copy()

        args["registry"] = self.base_repository

        return args

    def get_skip_tags(self) -> list[str]:
        return make_list_of_str(self.skip_tags)

    def get_include_tags(self) -> list[str]:
        return make_list_of_str(self.include_tags)

    def is_release_step_executed(self) -> bool:
        if "release" in self.get_skip_tags():
            return False
        if "release" in self.get_include_tags():
            return True
        return len(self.get_include_tags()) == 0


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
    builder: str,
    parallel: bool,
    debug: bool,
    architecture: Optional[List[str]] = None,
    sign: bool = False,
    all_agents: bool = False,
    parallel_factor: int = 0,
    agent_to_build: str = "",
) -> BuildConfiguration:
    bc = BuildConfiguration(
        image_type=os.environ.get("distro", DEFAULT_IMAGE_TYPE),
        base_repository=os.environ["BASE_REPO_URL"],
        namespace=os.environ.get("namespace", DEFAULT_NAMESPACE),
        skip_tags=make_list_of_str(os.environ.get("skip_tags")),
        include_tags=make_list_of_str(os.environ.get("include_tags")),
        builder=builder,
        parallel=parallel,
        all_agents=all_agents or bool(os.environ.get("all_agents", False)),
        debug=debug,
        architecture=architecture,
        sign=sign,
        parallel_factor=parallel_factor,
        agent_to_build=agent_to_build,
    )

    logger.info(f"is_running_in_patch: {is_running_in_patch()}")
    logger.info(f"is_running_in_evg_pipeline: {is_running_in_evg_pipeline()}")
    if is_running_in_patch() or not is_running_in_evg_pipeline():
        logger.info(
            f"Running build not in evg pipeline (is_running_in_evg_pipeline={is_running_in_evg_pipeline()}) "
            f"or in pipeline but not from master (is_running_in_patch={is_running_in_patch()}). "
            "Adding 'master' tag to skip to prevent publishing to the latest dev image."
        )
        bc.skip_tags.append("master")

    return bc


def is_running_in_evg_pipeline():
    return os.getenv("RUNNING_IN_EVG", "") == "true"


class MissingEnvironmentVariable(Exception):
    pass


def should_pin_at() -> Optional[Tuple[str, str]]:
    """Gets the value of the pin_tag_at to tag the images with.

    Returns its value split on :.
    """
    # We need to return something so `partition` does not raise
    # AttributeError
    is_patch = is_running_in_patch()

    try:
        pinned = os.environ["pin_tag_at"]
    except KeyError:
        raise MissingEnvironmentVariable(f"pin_tag_at environment variable does not exist, but is required")
    if is_patch:
        if pinned == "00:00":
            raise Exception("Pinning to midnight during a patch is not supported. Please pin to another date!")

    hour, _, minute = pinned.partition(":")
    return hour, minute


def is_running_in_patch():
    is_patch = os.environ.get("is_patch")
    return is_patch is not None and is_patch.lower() == "true"


def build_id() -> str:
    """Returns the current UTC time in ISO8601 date format.

    If running in Evergreen and `created_at` expansion is defined, use the
    datetime defined in that variable instead.

    It is possible to pin this time at midnight (00:00) for periodic builds. If
    running a manual build, then the Evergreen `pin_tag_at` variable needs to be
    set to the empty string, in which case, the image tag suffix will correspond
    to the current timestamp.

    """

    date = datetime.now(timezone.utc)
    try:
        created_at = os.environ["created_at"]
        date = datetime.strptime(created_at, "%y_%m_%d_%H_%M_%S")
    except KeyError:
        pass

    hour, minute = should_pin_at()
    if hour and minute:
        logger.info(f"we are pinning to, hour: {hour}, minute: {minute}")
        date = date.replace(hour=int(hour), minute=int(minute), second=0)
    else:
        logger.warning(f"hour and minute cannot be extracted from provided pin_tag_at env, pinning to now")

    string_time = date.strftime("%Y%m%dT%H%M%SZ")

    return string_time


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def get_git_release_tag() -> str:
    """Returns the git tag of the current run on releases, on non-release returns the patch id."""
    release_env_var = os.getenv("triggered_by_git_tag")

    # that means we are in a release and only return the git_tag; otherwise we want to return the patch_id
    # appended to ensure the image created is unique and does not interfere
    if release_env_var is not None:
        logger.info(f"git tag detected: {release_env_var}")
        return release_env_var

    patch_id = os.environ.get("version_id", "latest")
    logger.info(f"No git tag detected, using patch_id: {patch_id}")
    return patch_id


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
    docker_cmd = shutil.which("docker")
    if docker_cmd is None:
        raise Exception("Docker executable not found in PATH")

    final_manifest = image + ":" + tag

    args = [
        docker_cmd,
        "manifest",
        "create",
        final_manifest,
    ]

    for arch in architectures:
        args.extend(["--amend", f"{final_manifest}-{arch}"])

    args_str = " ".join(args)
    logger.debug(f"creating new manifest: {args_str}")
    cp = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    if cp.returncode != 0:
        raise Exception(cp.stderr)

    args = [docker_cmd, "manifest", "push", final_manifest]
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
def sonar_build_image(
    image_name: str,
    build_configuration: BuildConfiguration,
    args: Dict[str, str] = None,
    inventory="inventory.yaml",
    with_sbom: bool = True,
):
    """Calls sonar to build `image_name` with arguments defined in `args`."""
    span = trace.get_current_span()
    span.set_attribute("mck.image_name", image_name)
    span.set_attribute("mck.inventory", inventory)
    if args:
        span.set_attribute("mck.build_args", str(args))

    build_options = {
        # Will continue building an image if it finds an error. See next comment.
        "continue_on_errors": True,
        # But will still fail after all the tasks have completed
        "fail_on_errors": True,
        "pipeline": build_configuration.pipeline,
    }

    logger.info(f"Sonar config bc: {build_configuration}, args: {args}, for image: {image_name}")

    process_image(
        image_name,
        skip_tags=build_configuration.get_skip_tags(),
        include_tags=build_configuration.get_include_tags(),
        build_args=build_configuration.build_args(args),
        inventory=inventory,
        build_options=build_options,
    )

    if with_sbom:
        produce_sbom(build_configuration, args)


@TRACER.start_as_current_span("produce_sbom")
def produce_sbom(build_configuration, args):
    span = trace.get_current_span()
    if not is_running_in_evg_pipeline():
        logger.info("Skipping SBOM Generation (enabled only for EVG)")
        return

    try:
        image_pull_spec = args["quay_registry"] + args.get("ubi_suffix", "")
    except KeyError:
        logger.error(f"Could not find image pull spec. Args: {args}, BuildConfiguration: {build_configuration}")
        logger.error(f"Skipping SBOM generation")
        return

    try:
        image_tag = args["release_version"]
        span.set_attribute("mck.release_version", image_tag)
    except KeyError:
        logger.error(f"Could not find image tag. Args: {args}, BuildConfiguration: {build_configuration}")
        logger.error(f"Skipping SBOM generation")
        return

    image_pull_spec = f"{image_pull_spec}:{image_tag}"
    print(f"Producing SBOM for image: {image_pull_spec} args: {args}")

    if "platform" in args:
        if args["platform"] == "arm64":
            platform = "linux/arm64"
        elif args["platform"] == "amd64":
            platform = "linux/amd64"
        else:
            # TODO: return here?
            logger.error(f"Unrecognized architectures in {args}. Skipping SBOM generation")
    else:
        platform = "linux/amd64"

    generate_sbom(image_pull_spec, platform)


def build_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run tests.
    """
    image_name = "test"

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

    python_version = os.getenv("PYTHON_VERSION", "")
    if python_version == "":
        raise Exception("Missing PYTHON_VERSION environment variable")

    buildargs = dict({"python_version": python_version})

    sonar_build_image(image_name, build_configuration, buildargs, "inventories/test.yaml")


def build_mco_tests_image(build_configuration: BuildConfiguration):
    """
    Builds image used to run community tests.
    """
    image_name = "community-operator-e2e"
    golang_version = os.getenv("GOLANG_VERSION", "1.24")
    if golang_version == "":
        raise Exception("Missing GOLANG_VERSION environment variable")

    buildargs = dict({"golang_version": golang_version})

    sonar_build_image(image_name, build_configuration, buildargs, "inventories/mco_test.yaml")


TRACER.start_as_current_span("build_operator_image")


def build_operator_image(build_configuration: BuildConfiguration):
    """Calculates arguments required to build the operator image, and starts the build process."""
    # In evergreen, we can pass test_suffix env to publish the operator to a quay
    # repository with a given suffix.
    test_suffix = os.environ.get("test_suffix", "")
    log_automation_config_diff = os.environ.get("LOG_AUTOMATION_CONFIG_DIFF", "false")
    version = get_git_release_tag()

    # Use only amd64 if we should skip arm64 builds
    if should_skip_arm64(build_configuration):
        architectures = ["amd64"]
        logger.info("Skipping ARM64 builds for operator image as this is running in EVG pipeline as a patch")
    else:
        architectures = build_configuration.architecture or ["amd64", "arm64"]

    multi_arch_args_list = []

    for arch in architectures:
        arch_args = {
            "version": version,
            "log_automation_config_diff": log_automation_config_diff,
            "test_suffix": test_suffix,
            "debug": build_configuration.debug,
            "architecture": arch,
        }
        multi_arch_args_list.append(arch_args)

    logger.info(f"Building Operator args: {multi_arch_args_list}")

    image_name = "mongodb-kubernetes"

    current_span = trace.get_current_span()
    current_span.set_attribute("mck.image_name", image_name)
    current_span.set_attribute("mck.architecture", architectures)

    build_image_generic(
        config=build_configuration,
        image_name=image_name,
        inventory_file="inventory.yaml",
        multi_arch_args_list=multi_arch_args_list,
        with_image_base=False,
        is_multi_arch=True,
    )


def build_database_image(build_configuration: BuildConfiguration):
    """
    Builds a new database image.
    """
    release = get_release()
    version = release["databaseImageVersion"]
    args = {"version": version}
    build_image_generic(build_configuration, "database", "inventories/database.yaml", args)


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


def build_operator_image_patch(build_configuration: BuildConfiguration):
    """This function builds the operator locally and pushed into an existing
    Docker image. This is the fastest way I could image we can do this."""

    client = docker.from_env()
    # image that we know is where we build operator.
    image_repo = build_configuration.base_repository + "/" + build_configuration.image_type + "/mongodb-kubernetes"
    image_tag = "latest"
    repo_tag = image_repo + ":" + image_tag

    logger.debug(f"Pulling image: {repo_tag}")
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
        logger.info("Current operator image is too old, will rebuild it completely first")
        build_operator_image(build_configuration)
        return

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


def get_supported_variants_for_image(image: str) -> List[str]:
    return get_release()["supportedImages"][image]["variants"]


def image_config(
    image_name: str,
    name_prefix: str = "mongodb-kubernetes-",
    s3_bucket: str = "enterprise-operator-dockerfiles",
    ubi_suffix: str = "-ubi",
    base_suffix: str = "",
) -> Tuple[str, Dict[str, str]]:
    """Generates configuration for an image suitable to be passed
    to Sonar.

    It returns a dictionary with registries and S3 configuration."""
    args = {
        "quay_registry": "{}/{}{}".format(QUAY_REGISTRY_URL, name_prefix, image_name),
        "ecr_registry_ubi": "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/{}{}".format(name_prefix, image_name),
        "s3_bucket_http": "https://{}.s3.amazonaws.com/dockerfiles/{}{}".format(s3_bucket, name_prefix, image_name),
        "ubi_suffix": ubi_suffix,
        "base_suffix": base_suffix,
    }

    return image_name, args


def args_for_daily_image(image_name: str) -> Dict[str, str]:
    """Returns configuration for an image to be able to be pushed with Sonar.

    This includes the quay_registry and ospid corresponding to RedHat's project id.
    """
    image_configs = [
        image_config("database", ubi_suffix=""),
        image_config("init-appdb", ubi_suffix=""),
        image_config("agent", name_prefix="mongodb-enterprise-"),
        image_config("init-database", ubi_suffix=""),
        image_config("init-ops-manager", ubi_suffix=""),
        image_config("mongodb-kubernetes", name_prefix="", ubi_suffix=""),
        image_config("ops-manager", name_prefix="mongodb-enterprise-"),
        image_config(
            image_name="mongodb-kubernetes-operator",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
            # community ubi image does not have a suffix in its name
            ubi_suffix="",
        ),
        image_config(
            image_name="readinessprobe",
            ubi_suffix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
        image_config(
            image_name="operator-version-upgrade-post-start-hook",
            ubi_suffix="",
            s3_bucket="enterprise-operator-dockerfiles",
        ),
        image_config(
            image_name="mongodb-agent",
            name_prefix="",
            s3_bucket="enterprise-operator-dockerfiles",
            ubi_suffix="-ubi",
            base_suffix="-ubi",
        ),
    ]

    images = {k: v for k, v in image_configs}
    return images[image_name]


def is_version_in_range(version: str, min_version: str, max_version: str) -> bool:
    """Check if the version is in the range"""
    try:
        parsed_version = semver.VersionInfo.parse(version)
        if parsed_version.prerelease:
            logger.info(f"Excluding {version} from range {min_version}-{max_version} because it's a pre-release")
            return False
        version_without_rc = semver.VersionInfo.finalize_version(parsed_version)
    except ValueError:
        version_without_rc = version
    if min_version and max_version:
        return version_without_rc.match(">=" + min_version) and version_without_rc.match("<" + max_version)
    return True


def get_versions_to_rebuild(supported_versions, min_version, max_version):
    # this means we only want to release one version, we cannot rely on the below range function
    # since the agent does not follow semver for comparison
    if (min_version and max_version) and (min_version == max_version):
        return [min_version]
    return filter(lambda x: is_version_in_range(x, min_version, max_version), supported_versions)


def get_versions_to_rebuild_per_operator_version(supported_versions, operator_version):
    """
    This function returns all versions sliced by a specific operator version.
    If the input is `onlyAgents` then it only returns agents without the operator suffix.
    """
    versions_to_rebuild = []

    for version in supported_versions:
        if operator_version == "onlyAgents":
            # 1_ works because we append the operator version via "_", all agents end with "1".
            if "1_" not in version:
                versions_to_rebuild.append(version)
        else:
            if operator_version in version:
                versions_to_rebuild.append(version)
    return versions_to_rebuild


class TracedThreadPoolExecutor(ThreadPoolExecutor):
    """Implementation of :class:ThreadPoolExecutor that will pass context into sub tasks."""

    def __init__(self, tracer: Tracer, *args, **kwargs):
        self.tracer = tracer
        super().__init__(*args, **kwargs)

    def with_otel_context(self, c: otel_context.Context, fn: Callable):
        otel_context.attach(c)
        return fn()

    def submit(self, fn, *args, **kwargs):
        """Submit a new task to the thread pool."""

        # get the current otel context
        c = otel_context.get_current()
        if c:
            return super().submit(
                lambda: self.with_otel_context(c, lambda: fn(*args, **kwargs)),
            )
        else:
            return super().submit(lambda: fn(*args, **kwargs))


def should_skip_arm64(config: BuildConfiguration) -> bool:
    """
        Determines if arm64 builds should be skipped based on environment.
    Determines if arm64 builds should be skipped based on BuildConfiguration or environment.```
    And skipping the evergreen detail.
    """
    if config.is_release_step_executed():
        return False

    return is_running_in_evg_pipeline() and is_running_in_patch()


def build_image_daily(
    image_name: str,  # corresponds to the image_name in the release.json
    min_version: str = None,
    max_version: str = None,
    operator_version: str = None,
):
    """
    Starts the daily build process for an image. This function works for all images we support, for community and
    enterprise operator. The list of supported image_name is defined in get_builder_function_for_image_name.
    Builds an image for each version listed in ./release.json
    The registry used to pull base image and output the daily build is configured in the image_config function, it is passed
    as an argument to the inventories/daily.yaml file.

    If the context image supports both ARM and AMD architectures, both will be built.
    """

    def get_architectures_set(build_configuration, args):
        """Determine the set of architectures to build for"""
        arch_set = set(build_configuration.architecture) if build_configuration.architecture else set()
        if arch_set == {"arm64"}:
            raise ValueError("Building for ARM64 only is not supported yet")

        if should_skip_arm64(build_configuration):
            logger.info("Skipping ARM64 builds as this is running in as a patch and not a release step.")
            return {"amd64"}

        # Automatic architecture detection is the default behavior if 'arch' argument isn't specified
        if arch_set == set():
            if check_multi_arch(
                image=args["quay_registry"] + args["ubi_suffix"] + ":" + args["release_version"],
                suffix="-context",
            ):
                arch_set = {"amd64", "arm64"}
            else:
                # When nothing specified and single-arch, default to amd64
                arch_set = {"amd64"}

        return arch_set

    def create_and_push_manifests(args: dict, architectures: list[str]):
        """Create and push manifests for all registries."""
        registries = [args["ecr_registry_ubi"], args["quay_registry"]]
        tags = [args["release_version"], args["release_version"] + "-b" + args["build_id"]]
        for registry in registries:
            for tag in tags:
                create_and_push_manifest(registry + args["ubi_suffix"], tag, architectures=architectures)

    def sign_image_concurrently(executor, args, futures, arch=None):
        v = args["release_version"]
        logger.info(f"Enqueuing signing task for version: {v}")
        future = executor.submit(sign_image_in_repositories, args, arch)
        futures.append(future)

    @TRACER.start_as_current_span("inner")
    def inner(build_configuration: BuildConfiguration):
        supported_versions = get_supported_version_for_image_matrix_handling(image_name)
        variants = get_supported_variants_for_image(image_name)

        args = args_for_daily_image(image_name)
        args["build_id"] = build_id()

        completed_versions = set()

        filtered_versions = get_versions_to_rebuild(supported_versions, min_version, max_version)
        if operator_version:
            filtered_versions = get_versions_to_rebuild_per_operator_version(filtered_versions, operator_version)

        logger.info("Building Versions for {}: {}".format(image_name, filtered_versions))

        with TracedThreadPoolExecutor(TRACER) as executor:
            futures = []
            for version in filtered_versions:
                build_configuration = copy.deepcopy(build_configuration)
                if build_configuration.include_tags is None:
                    build_configuration.include_tags = []
                build_configuration.include_tags.extend(variants)

                logger.info("Rebuilding {} with variants {}".format(version, variants))
                args["release_version"] = version

                arch_set = get_architectures_set(build_configuration, args)
                span = trace.get_current_span()
                span.set_attribute("mck.architectures", str(arch_set))
                span.set_attribute("mck.architectures_numbers", len(arch_set))
                span.set_attribute("mck.release", build_configuration.is_release_step_executed())

                if version not in completed_versions:
                    if arch_set == {"amd64", "arm64"}:
                        # We need to release the non context amd64 and arm64 images first before we can create the sbom
                        for arch in arch_set:
                            # Suffix to append to images name for multi-arch (see usage in daily.yaml inventory)
                            args["architecture_suffix"] = f"-{arch}"
                            args["platform"] = arch
                            sonar_build_image(
                                "image-daily-build",
                                build_configuration,
                                args,
                                inventory="inventories/daily.yaml",
                                with_sbom=False,
                            )
                            if build_configuration.sign:
                                sign_image_concurrently(executor, copy.deepcopy(args), futures, arch)
                        create_and_push_manifests(args, list(arch_set))
                        for arch in arch_set:
                            args["architecture_suffix"] = f"-{arch}"
                            args["platform"] = arch
                            logger.info(f"Enqueuing SBOM production task for image: {version}")
                            future = executor.submit(produce_sbom, build_configuration, copy.deepcopy(args))
                            futures.append(future)
                        if build_configuration.sign:
                            sign_image_concurrently(executor, copy.deepcopy(args), futures)
                    else:
                        # No suffix for single arch images
                        args["architecture_suffix"] = ""
                        args["platform"] = "amd64"
                        sonar_build_image(
                            "image-daily-build",
                            build_configuration,
                            args,
                            inventory="inventories/daily.yaml",
                        )
                        if build_configuration.sign:
                            sign_image_concurrently(executor, copy.deepcopy(args), futures)
                    completed_versions.add(version)

            # wait for all signings to be done
            logger.info("Waiting for all tasks to complete...")
            encountered_error = False
            # all the futures contain concurrent sbom and signing tasks
            for future in futures:
                try:
                    future.result()
                except Exception as e:
                    logger.error(f"Error in future: {e}")
                    encountered_error = True

            executor.shutdown(wait=True)
            logger.info("All tasks completed.")

            # we execute them concurrently with retries, if one of them eventually fails, we fail the whole task
            if encountered_error:
                logger.info("Some tasks failed.")
                exit(1)

    return inner


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
    build_image_generic(build_configuration, "init-ops-manager", "inventories/init_om.yaml", args)


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
        config=build_configuration,
        image_name="ops-manager",
        inventory_file="inventories/om.yaml",
        extra_args=args,
        registry_address_override=f"{QUAY_REGISTRY_URL}/mongodb-enterprise-ops-manager",
    )


@TRACER.start_as_current_span("build_image_generic")
def build_image_generic(
    config: BuildConfiguration,
    image_name: str,
    inventory_file: str,
    extra_args: dict = None,
    with_image_base: bool = True,
    is_multi_arch: bool = False,
    multi_arch_args_list: list = None,
    is_run_in_parallel: bool = False,
    registry_address_override: str = "",
):
    """Build image generic builds context images and is used for triggering release. During releases
    it signs and verifies the context image.
    The release process uses the daily images build process.
    The with_image_base parameter determines whether the image being built should include a base image prefix.
    When set to True, the function prepends "mongodb-kubernetes-" to the image name
    """
    image_base = ""
    if with_image_base:
        image_base = "mongodb-kubernetes-"

    if not multi_arch_args_list:
        multi_arch_args_list = [extra_args or {}]
    version = multi_arch_args_list[0].get("version", "")

    if config.is_release_step_executed():
        registry = f"{QUAY_REGISTRY_URL}/{image_base}{image_name}"
    else:
        registry = f"{config.base_repository}/{image_base}{image_name}"

    if registry_address_override:
        registry = registry_address_override

    try:
        for args in multi_arch_args_list:  # in case we are building multiple architectures
            args["quay_registry"] = registry
            sonar_build_image(image_name, config, args, inventory_file, False)
        if is_multi_arch:
            # we only push the manifests of the context images here,
            # since daily rebuilds will push the manifests for the proper images later
            architectures = [v["architecture"] for v in multi_arch_args_list]
            create_and_push_manifest(registry, f"{version}-context", architectures=architectures)
            if not config.is_release_step_executed():
                # Normally daily rebuild would create and push the manifests for the non-context images.
                # But since we don't run daily rebuilds on ecr image builds, we can do that step instead here.
                # We only need to push manifests for multi-arch images.
                create_and_push_manifest(registry, version, architectures=architectures)
                latest_tag = "latest"
                if not is_running_in_patch() and is_running_in_evg_pipeline():
                    logger.info(f"Tagging and pushing {registry}:{version} as {latest_tag}")
                    try:
                        client = docker.from_env()
                        source_image = client.images.pull(f"{registry}:{version}")
                        source_image.tag(registry, latest_tag)
                        client.images.push(registry, tag=latest_tag)
                        span = trace.get_current_span()
                        span.set_attribute("mck.image.push_latest", f"{registry}:{latest_tag}")
                        logger.info(f"Successfully tagged and pushed {registry}:{latest_tag}")
                    except docker.errors.DockerException as e:
                        logger.error(f"Failed to tag/push {latest_tag} image: {e}")
                        raise
                else:
                    logger.info(
                        f"Skipping tagging and pushing {registry}:{version} as {latest_tag} tag; is_running_in_patch={is_running_in_patch()}, is_running_in_evg_pipeline={is_running_in_evg_pipeline()}"
                    )

        # Sign and verify the context image if on releases if required.
        if config.sign and config.is_release_step_executed():
            sign_and_verify_context_image(registry, version)

        span = trace.get_current_span()
        span.set_attribute("mck.image.image_name", image_name)
        span.set_attribute("mck.image.version", version)
        span.set_attribute("mck.image.is_release", config.is_release_step_executed())
        span.set_attribute("mck.image.is_multi_arch", is_multi_arch)

        if config.is_release_step_executed() and version and QUAY_REGISTRY_URL in registry:
            logger.info(
                f"finished building context images, releasing them now via daily builds process for"
                f" image: {image_name} and version: {version}!"
            )
            if is_run_in_parallel:
                time.sleep(random.uniform(0, 5))
            build_image_daily(image_name, version, version)(config)

    except Exception as e:
        logger.error(f"Error during build_image_generic for image {image_name}: {e}")
        logger.error(f"Full traceback for build_image_generic error:")
        for line in traceback.format_exception(type(e), e, e.__traceback__):
            logger.error(line.rstrip())
        raise


def sign_and_verify_context_image(registry, version):
    sign_image(registry, version + "-context")
    verify_signature(registry, version + "-context")


def build_init_appdb(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initAppDbVersion"]
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "is_appdb": True, "mongodb_tools_url_ubi": mongodb_tools_url_ubi}
    build_image_generic(build_configuration, "init-appdb", "inventories/init_appdb.yaml", args)


def build_community_image(build_configuration: BuildConfiguration, image_type: str):
    """
    Builds image for community components (readiness probe, upgrade hook).

    Args:
        build_configuration: The build configuration to use
        image_type: Type of image to build ("readiness-probe" or "upgrade-hook")
    """

    if image_type == "readiness-probe":
        image_name = "mongodb-kubernetes-readinessprobe"
        inventory_file = "inventories/readiness_probe.yaml"
    elif image_type == "upgrade-hook":
        image_name = "mongodb-kubernetes-operator-version-upgrade-post-start-hook"
        inventory_file = "inventories/upgrade_hook.yaml"
    else:
        raise ValueError(f"Unsupported image type: {image_type}")

    version = get_git_release_tag()
    golang_version = os.getenv("GOLANG_VERSION", "1.24")

    # Use only amd64 if we should skip arm64 builds
    if should_skip_arm64(build_configuration):
        architectures = ["amd64"]
        logger.info("Skipping ARM64 builds for community image as this is running in EVG pipeline as a patch")
    else:
        architectures = build_configuration.architecture or ["amd64", "arm64"]

    multi_arch_args_list = []

    for arch in architectures:
        arch_args = {
            "version": version,
            "golang_version": golang_version,
            "architecture": arch,
        }
        multi_arch_args_list.append(arch_args)

    build_image_generic(
        config=build_configuration,
        image_name=image_name,
        with_image_base=False,
        multi_arch_args_list=multi_arch_args_list,
        inventory_file=inventory_file,
        is_multi_arch=True,  # We for pushing manifest anyway, even if arm64 is skipped in patches
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
    is_release = build_configuration.is_release_step_executed()
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
    if not should_skip_arm64(build_configuration):
        joined_args.append(args | arch_arm)

    build_image_generic(
        config=build_configuration,
        image_name="mongodb-agent",
        inventory_file="inventories/agent.yaml",
        multi_arch_args_list=joined_args,
        with_image_base=False,
        is_multi_arch=True,  # We for pushing manifest anyway, even if arm64 is skipped in patches
        is_run_in_parallel=True,
    )


def build_agent_default_case(build_configuration: BuildConfiguration):
    """
    Build the agent only for the latest operator for patches and operator releases.

    See more information in the function: build_agent_on_agent_bump
    """
    release_json = get_release()

    is_release = build_configuration.is_release_step_executed()

    # We need to release [all agents x latest operator] on operator releases
    if is_release:
        agent_versions_to_build = gather_all_supported_agent_versions(release_json)
    # We only need [latest agents (for each OM major version and for CM) x patch ID] for patches
    else:
        agent_versions_to_build = gather_latest_agent_versions(release_json, build_configuration.agent_to_build)

    logger.info(f"Building Agent versions: {agent_versions_to_build}")

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
            _add_to_agent_queue(agent_version, build_configuration, executor, tasks_queue)

    queue_exception_handling(tasks_queue)


def build_agent_on_agent_bump(build_configuration: BuildConfiguration):
    """
    Build the agent matrix (operator version x agent version), triggered by PCT.

    We have three cases where we need to build the agent:
    - e2e test runs
    - operator releases
    - OM/CM bumps via PCT

    In OM/CM bumps, we release a new agent.
    """
    release_json = get_release()
    is_release = build_configuration.is_release_step_executed()

    if build_configuration.all_agents:
        agent_versions_to_build = gather_all_supported_agent_versions(release_json)
    else:
        # we only need to release the latest images, we don't need to re-push old images, as we don't clean them up anymore.
        agent_versions_to_build = gather_latest_agent_versions(release_json, build_configuration.agent_to_build)

    legacy_agent_versions_to_build = release_json["supportedImages"]["mongodb-agent"]["versions"]

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
            logger.info(f"Building Agent versions: {agent_version}")
            _add_to_agent_queue(agent_version, build_configuration, executor, tasks_queue)

    queue_exception_handling(tasks_queue)


@TRACER.start_as_current_span("queue_exception_handling")
def queue_exception_handling(tasks_queue):
    span = trace.get_current_span()

    exceptions_found = False
    exception_count = 0
    total_tasks = len(tasks_queue.queue)
    exception_types = set()

    span.set_attribute("mck.agent.queue.tasks_total", total_tasks)

    for task in tasks_queue.queue:
        if task.exception() is not None:
            exceptions_found = True
            exception_count += 1
            exception_types.add(type(task.exception()).__name__)

            exception_info = task.exception()
            logger.fatal(f"=== THREAD EXCEPTION DETAILS ===")
            logger.fatal(f"Exception Type: {type(exception_info).__name__}")
            logger.fatal(f"Exception Message: {str(exception_info)}")
            logger.fatal(f"=== END THREAD EXCEPTION DETAILS ===")

    span.set_attribute("mck.agent.queue.exceptions_count", exception_count)
    span.set_attribute(
        "mck.agent.queue.success_rate", ((total_tasks - exception_count) / total_tasks * 100) if total_tasks > 0 else 0
    )
    span.set_attribute("mck.agent.queue.exception_types", list(exception_types))
    span.set_attribute("mck.agent.queue.has_exceptions", exceptions_found)

    if exceptions_found:
        raise Exception(
            f"Exception(s) found when processing Agent images. \nSee also previous logs for more info\nFailing the build"
        )


def _add_to_agent_queue(
    agent_version: Tuple[str, str],
    build_configuration: BuildConfiguration,
    executor: ProcessPoolExecutor,
    tasks_queue: Queue,
):
    tools_version = agent_version[1]
    image_version = f"{agent_version[0]}"

    tasks_queue.put(
        executor.submit(
            build_multi_arch_agent_in_sonar,
            build_configuration,
            image_version,
            tools_version,
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


def gather_latest_agent_versions(release: Dict, agent_to_build: str = "") -> List[Tuple[str, str]]:
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

    if agent_to_build != "":
        for agent_tuple in agent_versions_to_build:
            if agent_tuple[0] == agent_to_build:
                return [agent_tuple]

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
        # Daily builds
        "operator-daily": build_image_daily("mongodb-kubernetes"),
        "appdb-daily": build_image_daily("appdb"),
        "database-daily": build_image_daily("database"),
        "init-appdb-daily": build_image_daily("init-appdb"),
        "init-database-daily": build_image_daily("init-database"),
        "init-ops-manager-daily": build_image_daily("init-ops-manager"),
        "ops-manager-6-daily": build_image_daily("ops-manager", min_version="6.0.0", max_version="7.0.0"),
        "ops-manager-7-daily": build_image_daily("ops-manager", min_version="7.0.0", max_version="8.0.0"),
        "ops-manager-8-daily": build_image_daily("ops-manager", min_version="8.0.0", max_version="9.0.0"),
        #
        # Ops Manager image
        "ops-manager": build_om_image,
        # This only builds the agents without the operator suffix
        "mongodb-agent-daily": build_image_daily("mongodb-agent", operator_version="onlyAgents"),
        # Community images
        "readinessprobe-daily": build_image_daily(
            "readinessprobe",
        ),
        "operator-version-upgrade-post-start-hook-daily": build_image_daily(
            "operator-version-upgrade-post-start-hook",
        ),
        "mongodb-kubernetes-operator-daily": build_image_daily("mongodb-kubernetes-operator"),
    }

    # since we only support the last 3 operator versions, we can build the following names which each matches to an
    # operator version we support and rebuild:
    # - mongodb-agent-daily-1
    # - mongodb-agent-daily-2
    # - mongodb-agent-daily-3
    # get_supported_operator_versions returns the last three supported operator versions in a sorted manner
    i = 1
    for operator_version in get_supported_operator_versions():
        image_builders[f"mongodb-agent-{i}-daily"] = build_image_daily(
            "mongodb-agent", operator_version=operator_version
        )
        i += 1

    return image_builders


# TODO: nam static: remove this once static containers becomes the default
def build_init_database(build_configuration: BuildConfiguration):
    release = get_release()
    version = release["initDatabaseVersion"]  # comes from release.json
    base_url = "https://fastdl.mongodb.org/tools/db/"
    mongodb_tools_url_ubi = "{}{}".format(base_url, release["mongodbToolsBundle"]["ubi"])
    args = {"version": version, "mongodb_tools_url_ubi": mongodb_tools_url_ubi, "is_appdb": False}
    build_image_generic(build_configuration, "init-database", "inventories/init_database.yaml", args)


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(
    images: Iterable[str],
    builder: str,
    debug: bool = False,
    parallel: bool = False,
    architecture: Optional[List[str]] = None,
    sign: bool = False,
    all_agents: bool = False,
    agent_to_build: str = "",
    parallel_factor: int = 0,
):
    """Builds all the images in the `images` list."""
    build_configuration = operator_build_configuration(
        builder, parallel, debug, architecture, sign, all_agents, parallel_factor, agent_to_build
    )
    if sign:
        mongodb_artifactory_login()
    for image in images:
        build_image(image, build_configuration)


def calculate_images_to_build(
    images: List[str], include: Optional[List[str]], exclude: Optional[List[str]]
) -> Set[str]:
    """
    Calculates which images to build based on the `images`, `include` and `exclude` sets.

    >>> calculate_images_to_build(["a", "b"], ["a"], ["b"])
    ... ["a"]
    """

    if not include and not exclude:
        return set(images)
    include = set(include or [])
    exclude = set(exclude or [])
    images = set(images or [])

    for image in include.union(exclude):
        if image not in images:
            raise ValueError("Image definition {} not found".format(image))

    images_to_build = include.intersection(images)
    if exclude:
        images_to_build = images.difference(exclude)
    return images_to_build


def main():
    _setup_tracing()

    parser = argparse.ArgumentParser()
    parser.add_argument("--include", action="append", help="list of images to include")
    parser.add_argument("--exclude", action="append", help="list of images to exclude")
    parser.add_argument("--builder", default="docker", type=str, help="docker or podman")
    parser.add_argument("--list-images", action="store_true")
    parser.add_argument("--parallel", action="store_true", default=False)
    parser.add_argument("--debug", action="store_true", default=False)
    parser.add_argument(
        "--arch",
        choices=["amd64", "arm64"],
        nargs="+",
        help="for daily builds only, specify the list of architectures to build for images",
    )
    parser.add_argument("--sign", action="store_true", default=False)
    parser.add_argument(
        "--parallel-factor",
        type=int,
        default=0,
        help="the factor on how many agents are build in parallel. 0 means all CPUs will be used",
    )
    parser.add_argument(
        "--all-agents",
        action="store_true",
        default=False,
        help="optional parameter to be able to push "
        "all non operator suffixed agents, even if we are not in a release",
    )
    parser.add_argument(
        "--build-one-agent",
        default="",
        help="optional parameter to push one agent",
    )
    args = parser.parse_args()

    if args.list_images:
        print(get_builder_function_for_image_name().keys())
        sys.exit(0)

    if args.arch == ["arm64"]:
        print("Building for arm64 only is not supported yet")
        sys.exit(1)

    if not args.sign:
        logger.warning("--sign flag not provided, images won't be signed")

    images_to_build = calculate_images_to_build(
        list(get_builder_function_for_image_name().keys()), args.include, args.exclude
    )

    build_all_images(
        images_to_build,
        args.builder,
        debug=args.debug,
        parallel=args.parallel,
        architecture=args.arch,
        sign=args.sign,
        all_agents=args.all_agents,
        agent_to_build=args.build_one_agent,
        parallel_factor=args.parallel_factor,
    )


if __name__ == "__main__":
    main()
