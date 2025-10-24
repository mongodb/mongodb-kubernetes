import argparse

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.build.image_build_configuration import SUPPORTED_PLATFORMS


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
