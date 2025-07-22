import argparse
import os
import sys
import time
from typing import Callable, Dict, Iterable, List, Optional

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
from scripts.evergreen.release.images_signing import mongodb_artifactory_login
from scripts.release.atomic_pipeline import (
    build_agent_default_case,
    build_agent_on_agent_bump,
    build_CLI_SBOM,
    build_database_image,
    build_init_appdb,
    build_init_database,
    build_init_om_image,
    build_mco_tests_image,
    build_om_image,
    build_operator_image,
    build_operator_image_patch,
    build_readiness_probe_image,
    build_tests_image,
    build_upgrade_hook_image,
)
from scripts.release.build_configuration import BuildConfiguration
from scripts.release.build_context import (
    BuildContext,
    RegistryResolver,
    VersionResolver,
)


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


def build_image(image_name: str, build_configuration: BuildConfiguration):
    """Builds one of the supported images by its name."""
    get_builder_function_for_image_name()[image_name](build_configuration)


def build_all_images(
    images: Iterable[str],
    build_configuration: BuildConfiguration,
):
    """Builds all the images in the `images` list."""
    # if sign:
    #    mongodb_artifactory_login()
    for idx, image in enumerate(images):
        logger.info(f"====Building image {image} ({idx + 1}/{len(images)})====")
        time.sleep(1)
        build_image(image, build_configuration)


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
    parser.add_argument("--include", action="append", help="Image to include.")
    parser.add_argument("--skip", action="append", help="Image to skip.")
    parser.add_argument(
        "--builder",
        default="docker",
        choices=["docker", "podman"],
        help="Tool to use to build images.",
    )
    parser.add_argument("--parallel", action="store_true", help="Build images in parallel.")
    parser.add_argument("--debug", action="store_true", help="Enable debug logging.")
    parser.add_argument("--sign", action="store_true", help="Sign images.")
    parser.add_argument(
        "--platform",
        default="linux/amd64",
        help="Target platforms for multi-arch builds (comma-separated). Example: linux/amd64,linux/arm64. Defaults to linux/amd64.",
    )
    parser.add_argument(
        "--all-agents",
        action="store_true",
        help="Build all agent variants instead of only the latest.",
    )
    parser.add_argument(
        "--parallel-factor",
        default=0,
        type=int,
        help="Number of builds to run in parallel, defaults to number of cores",
    )

    args = parser.parse_args()
    images_to_build = get_builder_function_for_image_name().keys()

    if args.include:
        images_to_build = set(args.include)

    if args.skip:
        images_to_build = set(images_to_build) - set(args.skip)

    # Parse platform argument (comma-separated)
    platforms = [p.strip() for p in args.platform.split(",")]

    # Centralized configuration management
    build_context = BuildContext.from_environment()
    version_resolver = VersionResolver(build_context)
    registry_resolver = RegistryResolver(build_context)

    build_configuration = BuildConfiguration(
        scenario=build_context.scenario,
        version=version_resolver.get_version(),
        base_registry=registry_resolver.get_base_registry(),
        parallel=args.parallel,
        debug=args.debug,
        architecture=platforms,
        sign=args.sign or build_context.signing_enabled,
        all_agents=args.all_agents or bool(os.environ.get("all_agents", False)),
        parallel_factor=args.parallel_factor,
    )

    logger.info(f"Building images: {list(images_to_build)}")
    logger.info(f"Build configuration: {build_configuration}")

    build_all_images(
        images=images_to_build,
        build_configuration=build_configuration,
    )


if __name__ == "__main__":
    main()
