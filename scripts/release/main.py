import argparse
import os
import sys
import time
from typing import Dict, Callable, Iterable, Optional, List

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
    build_CLI_SBOM,
    build_tests_image,
    build_operator_image,
    build_mco_tests_image,
    build_readiness_probe_image,
    build_upgrade_hook_image,
    build_operator_image_patch,
    build_database_image,
    build_agent_on_agent_bump,
    build_agent_default_case,
    build_init_appdb,
    build_init_database,
    build_init_om_image,
    build_om_image, operator_build_configuration,
)
from scripts.release.build_configuration import BuildConfiguration


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

    parser = argparse.ArgumentParser()
    parser.add_argument("--include", action="append", help="list of images to include")
    parser.add_argument("--list-images", action="store_true")
    parser.add_argument("--parallel", action="store_true", default=False)
    parser.add_argument("--debug", action="store_true", default=False)
    parser.add_argument(
        "--arch",
        choices=["amd64", "arm64"],
        nargs="+",
        help="for operator and community images only, specify the list of architectures to build for images",
    )
    parser.add_argument("--sign", action="store_true", default=False)
    parser.add_argument(
        "--parallel-factor",
        type=int,
        default=0,
        help="the factor on how many agents are built in parallel. 0 means all CPUs will be used",
    )
    parser.add_argument(
        "--all-agents",
        action="store_true",
        default=False,
        help="optional parameter to be able to push "
        "all non operator suffixed agents, even if we are not in a release",
    )
    args = parser.parse_args()

    images_list = get_builder_function_for_image_name().keys()

    if args.list_images:
        print(images_list)
        sys.exit(0)

    if not args.include:
        logger.error(f"--include is required")
        sys.exit(1)

    if args.arch == ["arm64"]:
        print("Building for arm64 only is not supported yet")
        sys.exit(1)

    if not args.sign:
        logger.warning("--sign flag not provided, images won't be signed")

    # TODO check that image names are valid
    images_to_build = sorted(list(set(args.include).intersection(images_list)))
    if not images_to_build:
        logger.error("No images to build, please ensure images names are correct.")
        sys.exit(1)

    TEMP_HARDCODED_BASE_REGISTRY = "268558157000.dkr.ecr.us-east-1.amazonaws.com/julienben/staging-temp"

    build_all_images(
        base_registry=TEMP_HARDCODED_BASE_REGISTRY,
        images=images_to_build,
        debug=args.debug,
        parallel=args.parallel,
        architecture=args.arch,
        sign=args.sign,
        all_agents=args.all_agents,
        parallel_factor=args.parallel_factor,
    )


if __name__ == "__main__":
    main()
