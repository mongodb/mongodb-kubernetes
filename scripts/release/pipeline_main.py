import argparse
import os
import sys
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
    build_init_appdb,
    build_init_database,
    build_init_om_image,
    build_mco_tests_image,
    build_om_image,
    build_operator_image,
    build_readiness_probe_image,
    build_tests_image,
    build_upgrade_hook_image,
)
from scripts.release.build_configuration import BuildConfiguration
from scripts.release.build_context import (
    BuildContext,
    BuildScenario,
)

"""
The goal of main.py, build_configuration.py and build_context.py is to provide a single source of truth for the build
configuration. All parameters that depend on the the build environment (local dev, evg, etc) should be resolved here and
not in the pipeline.
"""

SUPPORTED_PLATFORMS = ["linux/amd64", "linux/arm64"]


def get_builder_function_for_image_name() -> Dict[str, Callable]:
    """Returns a dictionary of image names that can be built."""

    image_builders = {
        "test": build_tests_image,
        "operator": build_operator_image,
        "mco-test": build_mco_tests_image,
        "readiness-probe": build_readiness_probe_image,
        "upgrade-hook": build_upgrade_hook_image,
        "database": build_database_image,
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


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


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
    parser.add_argument("--sign", action="store_true", help="Sign images.")
    parser.add_argument(
        "--scenario",
        choices=list(BuildScenario),
        help=f"Override the build scenario instead of inferring from environment. Options: release, patch, master, development",
    )
    # Override arguments for build context and configuration
    parser.add_argument(
        "--platform",
        default="linux/amd64",
        help="Target platforms for multi-arch builds (comma-separated). Example: linux/amd64,linux/arm64. Defaults to linux/amd64.",
    )
    parser.add_argument(
        "--version",
        help="Override the version/tag instead of resolving from build scenario",
    )
    parser.add_argument(
        "--registry",
        help="Override the base registry instead of resolving from build scenario",
    )
    # For agent builds
    parser.add_argument(
        "--parallel-factor",
        default=0,
        type=int,
        help="Number of builds to run in parallel, defaults to number of cores",
    )

    args = parser.parse_args()

    build_config = build_config_from_args(args)
    logger.info(f"Building image: {args.image}")
    logger.info(f"Build configuration: {build_config}")

    build_image(args.image, build_config)


def build_config_from_args(args):
    # Validate that the image name is supported
    supported_images = get_builder_function_for_image_name().keys()
    if args.image not in supported_images:
        logger.error(f"Unsupported image '{args.image}'. Supported images: {', '.join(supported_images)}")
        sys.exit(1)

    # Parse platform argument (comma-separated)
    platforms = [p.strip() for p in args.platform.split(",")]
    if any(p not in SUPPORTED_PLATFORMS for p in platforms):
        logger.error(
            f"Unsupported platform in '{args.platform}'. Supported platforms: {', '.join(SUPPORTED_PLATFORMS)}"
        )
        sys.exit(1)

    # Centralized configuration management with overrides
    build_scenario = args.scenario or BuildScenario.infer_scenario_from_environment()
    build_context = BuildContext.from_scenario(build_scenario)

    # Resolve final values with overrides
    scenario = args.scenario or build_context.scenario
    version = args.version or build_context.get_version()
    registry = args.registry or build_context.get_base_registry()
    sign = args.sign or build_context.signing_enabled

    return BuildConfiguration(
        scenario=scenario,
        version=version,
        base_registry=registry,
        parallel=args.parallel,
        platforms=platforms,
        sign=sign,
        parallel_factor=args.parallel_factor,
    )


if __name__ == "__main__":
    main()
