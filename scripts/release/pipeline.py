import argparse
import os
from functools import partial
from typing import Callable, Dict

from opentelemetry import context, trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import (
    OTLPSpanExporter as OTLPSpanGrpcExporter,
)
from opentelemetry.sdk.resources import SERVICE_NAME, Resource
from opentelemetry.sdk.trace import (
    SynchronousMultiSpanProcessor,
    TracerProvider,
)
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import NonRecordingSpan, SpanContext, TraceFlags

from lib.base_logger import logger
from scripts.release.atomic_pipeline import (
    build_agent,
    build_database_image,
    build_init_appdb_image,
    build_init_database_image,
    build_init_om_image,
    build_mco_tests_image,
    build_meko_tests_image,
    build_om_image,
    build_operator_image,
    build_readiness_probe_image,
    build_upgrade_hook_image,
)
from scripts.release.build.build_info import (
    AGENT_IMAGE,
    DATABASE_IMAGE,
    INIT_APPDB_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    MCO_TESTS_IMAGE,
    MEKO_TESTS_IMAGE,
    OPERATOR_IMAGE,
    OPERATOR_RACE_IMAGE,
    OPS_MANAGER_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
    load_build_info,
)
from scripts.release.build.build_scenario import (
    BuildScenario,
)
from scripts.release.build.image_build_configuration import (
    SUPPORTED_PLATFORMS,
    ImageBuildConfiguration,
)
from scripts.release.build.image_build_process import (
    DEFAULT_BUILDER_NAME,
    ensure_buildx_builder,
)

"""
The goal of main.py, image_build_configuration.py and build_context.py is to provide a single source of truth for the build
configuration. All parameters that depend on the the build environment (local dev, evg, etc) should be resolved here and
not in the pipeline.
"""


def get_builder_function_for_image_name() -> Dict[str, Callable]:
    """Returns a dictionary of image names that can be built."""

    image_builders = {
        MEKO_TESTS_IMAGE: build_meko_tests_image,
        OPERATOR_IMAGE: build_operator_image,
        OPERATOR_RACE_IMAGE: partial(build_operator_image, with_race_detection=True),
        MCO_TESTS_IMAGE: build_mco_tests_image,
        READINESS_PROBE_IMAGE: build_readiness_probe_image,
        UPGRADE_HOOK_IMAGE: build_upgrade_hook_image,
        DATABASE_IMAGE: build_database_image,
        AGENT_IMAGE: build_agent,
        # Init images
        INIT_APPDB_IMAGE: build_init_appdb_image,
        INIT_DATABASE_IMAGE: build_init_database_image,
        INIT_OPS_MANAGER_IMAGE: build_init_om_image,
        # Ops Manager image
        OPS_MANAGER_IMAGE: build_om_image,
    }

    return image_builders


def build_image(image_name: str, build_configuration: ImageBuildConfiguration):
    """Builds one of the supported images by its name."""
    if image_name not in get_builder_function_for_image_name():
        raise ValueError(
            f"Image '{image_name}' is not supported. Supported images: {', '.join(get_builder_function_for_image_name().keys())}"
        )
    get_builder_function_for_image_name()[image_name](build_configuration)


def image_build_config_from_args(args) -> ImageBuildConfiguration:
    image = args.image

    build_scenario = get_scenario_from_arg(args.build_scenario)
    build_info = load_build_info(build_scenario)
    logger.info(f"image is {image}")
    logger.info(f"images are {build_info.images}")
    image_build_info = build_info.images.get(image)
    logger.info(f"image_build_info is {image_build_info}")
    if not image_build_info:
        raise ValueError(f"Image '{image}' is not defined in the build info for scenario '{build_scenario}'")

    # Resolve final values with overrides
    version = args.version
    latest_tag = image_build_info.latest_tag
    olm_tag = image_build_info.olm_tag
    if args.registry:
        registries = [args.registry]
    else:
        registries = image_build_info.repositories
    platforms = get_platforms_from_arg(args.platform) or image_build_info.platforms
    sign = args.sign or image_build_info.sign
    dockerfile_path = image_build_info.dockerfile_path
    skip_if_exists = image_build_info.skip_if_exists

    # Validate version - only agent can have None version as the versions are managed by the agent
    # which are externally retrieved from release.json
    if version is None and image != "agent":
        raise ValueError(f"Version cannot be empty for {image}.")

    return ImageBuildConfiguration(
        scenario=build_scenario,
        version=version,
        latest_tag=latest_tag,
        olm_tag=olm_tag,
        registries=registries,
        dockerfile_path=dockerfile_path,
        platforms=platforms,
        sign=sign,
        skip_if_exists=skip_if_exists,
        parallel=args.parallel,
        parallel_factor=args.parallel_factor,
        all_agents=args.all_agents,
        currently_used_agents=args.current_agents,
    )


def get_scenario_from_arg(args_scenario: str) -> BuildScenario | None:
    try:
        return BuildScenario(args_scenario)
    except ValueError as e:
        raise ValueError(f"Invalid scenario '{args_scenario}': {e}")


def get_platforms_from_arg(args_platforms: str) -> list[str] | None:
    if not args_platforms:
        return None

    platforms = [p.strip() for p in args_platforms.split(",")]
    if any(p not in SUPPORTED_PLATFORMS for p in platforms):
        raise ValueError(
            f"Unsupported platform in --platforms '{args_platforms}'. Supported platforms: {', '.join(SUPPORTED_PLATFORMS)}"
        )
    return platforms


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
        # Magic number needed for our OTEL collector
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


def main():
    _setup_tracing()
    supported_images = list(get_builder_function_for_image_name().keys())
    supported_scenarios = list(BuildScenario)

    parser = argparse.ArgumentParser(
        description="""Builder tool for container images. It allows to push and sign images with multiple architectures using Docker Buildx.
By default build information is read from 'build_info.json' file in the project root directory based on the build scenario.""",
    )
    parser.add_argument(
        "image",
        metavar="image",
        action="store",
        type=str,
        choices=supported_images,
        help=f"Image name to build. Supported images: {", ".join(supported_images)}",
    )
    parser.add_argument(
        "-b",
        "--build-scenario",
        metavar="",
        action="store",
        required=True,
        type=str,
        choices=supported_scenarios,
        help=f"""Build scenario when reading configuration from 'build_info.json'.
Options: {", ".join(supported_scenarios)}. For '{BuildScenario.DEVELOPMENT}' the '{BuildScenario.PATCH}' scenario is used to read values from 'build_info.json'""",
    )
    parser.add_argument(
        "-p",
        "--platform",
        metavar="",
        action="store",
        type=str,
        help="Override the platforms instead of resolving from build scenario. Multi-arch builds are comma-separated. Example: linux/amd64,linux/arm64",
    )
    parser.add_argument(
        "-v",
        "--version",
        metavar="",
        action="store",
        type=str,
        help="Version to use when building container image. Required for all images except for agent where we read it from release.json",
    )
    parser.add_argument(
        "-r",
        "--registry",
        metavar="",
        action="store",
        type=str,
        help="Override the base registry instead of resolving from build scenario",
    )
    parser.add_argument(
        "-s",
        "--sign",
        action="store_true",
        help="If set force image signing. Default is to infer from build scenario.",
    )
    # For agent builds
    parser.add_argument(
        "--parallel",
        action="store_true",
        help="Build agent images in parallel.",
    )
    parser.add_argument(
        "--parallel-factor",
        metavar="",
        default=0,
        action="store",
        type=int,
        help="Number of agent builds to run in parallel, defaults to number of cores",
    )
    parser.add_argument(
        "--all-agents",
        action="store_true",
        help="Build all agent images.",
    )
    parser.add_argument(
        "--current-agents",
        action="store_true",
        help="Build all currently used agent images.",
    )

    args = parser.parse_args()

    build_config = image_build_config_from_args(args)
    logger.info(f"Building image: {args.image}")
    logger.info(f"Build configuration: {build_config}")

    # Create buildx builder
    # It must be initialized here as opposed to in build_images.py so that parallel calls (such as agent builds) can access it
    # and not face race conditions
    ensure_buildx_builder(DEFAULT_BUILDER_NAME)
    build_image(args.image, build_config)


if __name__ == "__main__":
    main()
