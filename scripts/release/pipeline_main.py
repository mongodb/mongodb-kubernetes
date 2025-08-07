import argparse
import os
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
    build_agent_default_case,
    build_database_image,
    build_init_appdb_image,
    build_init_database_image,
    build_init_om_image,
    build_mco_tests_image,
    build_om_image,
    build_operator_image,
    build_operator_image_patch,
    build_readiness_probe_image,
    build_tests_image,
    build_upgrade_hook_image,
)
from scripts.release.build.build_info import load_build_info
from scripts.release.build.build_scenario import (
    BuildScenario,
)
from scripts.release.build.image_build_configuration import (
    SUPPORTED_PLATFORMS,
    ImageBuildConfiguration,
)

"""
The goal of main.py, image_build_configuration.py and build_context.py is to provide a single source of truth for the build
configuration. All parameters that depend on the the build environment (local dev, evg, etc) should be resolved here and
not in the pipeline.
"""


def get_builder_function_for_image_name() -> Dict[str, Callable]:
    """Returns a dictionary of image names that can be built."""

    image_builders = {
        "meko-tests": build_tests_image,  # working
        "operator": build_operator_image,  # working
        "mco-tests": build_mco_tests_image,  # working
        "readiness-probe": build_readiness_probe_image,  # working, but still using single arch build
        "upgrade-hook": build_upgrade_hook_image,  # working, but still using single arch build
        "operator-quick": build_operator_image_patch,  # TODO: remove this image, it is not used anymore
        "database": build_database_image,  # working
        "agent-pct": build_agent_on_agent_bump,
        "agent": build_agent_default_case,
        # Init images
        "init-appdb": build_init_appdb_image,  # working
        "init-database": build_init_database_image,  # working
        "init-ops-manager": build_init_om_image,  # working
        # Ops Manager image
        "ops-manager": build_om_image,
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

    build_scenario = get_scenario_from_arg(args.scenario) or BuildScenario.infer_scenario_from_environment()

    build_info = load_build_info(build_scenario)
    logger.info(f"image is {image}")
    logger.info(f"images are {build_info.images}")
    image_build_info = build_info.images.get(image)
    logger.info(f"image_build_info is {image_build_info}")
    if not image_build_info:
        raise ValueError(f"Image '{image}' is not defined in the build info for scenario '{build_scenario}'")

    # Resolve final values with overrides
    version = args.version or image_build_info.version
    registry = args.registry or image_build_info.repository
    platforms = get_platforms_from_arg(args.platform) or image_build_info.platforms
    sign = args.sign or image_build_info.sign

    return ImageBuildConfiguration(
        scenario=build_scenario,
        version=version,
        registry=registry,
        parallel=args.parallel,
        platforms=platforms,
        sign=sign,
        parallel_factor=args.parallel_factor,
    )


def get_scenario_from_arg(args_scenario: str) -> BuildScenario | None:
    if not args_scenario:
        return None

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
    parser = argparse.ArgumentParser(description="Build container images.")
    parser.add_argument("image", help="Image to build.")  # Required
    parser.add_argument("--parallel", action="store_true", help="Build images in parallel.")
    parser.add_argument("--debug", action="store_true", help="Enable debug logging.")
    parser.add_argument(
        "--scenario",
        choices=list(BuildScenario),
        help=f"Override the build scenario instead of inferring from environment. Options: release, patch, master, development",
    )
    # Override arguments for build context and configuration
    parser.add_argument(
        "--platform",
        help="Override the platforms instead of resolving from build scenario",
    )
    parser.add_argument(
        "--version",
        help="Override the version/tag instead of resolving from build scenario",
    )
    parser.add_argument(
        "--registry",
        help="Override the base registry instead of resolving from build scenario",
    )
    parser.add_argument(
        "--sign", action="store_true", help="Force signing instead of resolving condition from build scenario"
    )

    # Agent specific arguments
    parser.add_argument(
        "--all-agents",
        action="store_true",
        help="Build all agent variants instead of only the latest",
    )
    parser.add_argument(
        "--parallel-factor",
        default=0,
        type=int,
        help="Number of builds to run in parallel, defaults to number of cores",
    )

    args = parser.parse_args()

    build_config = image_build_config_from_args(args)
    logger.info(f"Building image: {args.image}")
    logger.info(f"Build configuration: {build_config}")

    build_image(args.image, build_config)


if __name__ == "__main__":
    main()
