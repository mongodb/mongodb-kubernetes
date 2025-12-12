import argparse

from scripts.release.build.build_info import BUILDER_DOCKER, BUILDER_PODMAN
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.build.image_build_configuration import SUPPORTED_PLATFORMS
from scripts.release.build.image_build_process import DockerImageBuilder, PodmanImageBuilder


def str2bool(v):
    if isinstance(v, bool):
        return v
    if v.lower() in ("yes", "true", "t", "y", "1"):
        return True
    elif v.lower() in ("no", "false", "f", "n", "0"):
        return False
    else:
        raise argparse.ArgumentTypeError("Boolean value expected.")


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


def get_image_builder_from_arg(builder_name: str):
    if builder_name == BUILDER_DOCKER:
        return DockerImageBuilder()
    elif builder_name == BUILDER_PODMAN:
        return PodmanImageBuilder()
    else:
        raise ValueError(f"Unsupported image builder '{builder_name}'. Supported builders: docker, podman")
