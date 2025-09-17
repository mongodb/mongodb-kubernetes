import json
from dataclasses import dataclass
from typing import Dict, List

from scripts.release.build.build_scenario import BuildScenario

MEKO_TESTS_IMAGE = "meko-tests"
OPERATOR_IMAGE = "operator"
OPERATOR_RACE_IMAGE = "operator-race"
MCO_TESTS_IMAGE = "mco-tests"
READINESS_PROBE_IMAGE = "readiness-probe"
UPGRADE_HOOK_IMAGE = "upgrade-hook"
DATABASE_IMAGE = "database"
AGENT_IMAGE = "agent"
INIT_APPDB_IMAGE = "init-appdb"
INIT_DATABASE_IMAGE = "init-database"
INIT_OPS_MANAGER_IMAGE = "init-ops-manager"
OPS_MANAGER_IMAGE = "ops-manager"


@dataclass
class ImageInfo:
    repositories: List[str]
    platforms: list[str]
    dockerfile_path: str
    sign: bool = False
    latest_tag: bool = False
    olm_tag: bool = False
    skip_if_exists: bool = False


@dataclass
class BinaryInfo:
    s3_store: str
    platforms: list[str]
    sign: bool = False


@dataclass
class HelmChartInfo:
    repositories: List[str]
    sign: bool = False


@dataclass
class BuildInfo:
    images: Dict[str, ImageInfo]
    binaries: Dict[str, BinaryInfo]
    helm_charts: Dict[str, HelmChartInfo]


def load_build_info(scenario: BuildScenario) -> BuildInfo:
    f"""
    Load build information based on the specified scenario.

    :param scenario: BuildScenario enum value indicating the build scenario (e.g. "development", "patch", "staging", "release"). "development" scenario will return build info for "patch" scenario.
    :return: BuildInfo object containing images, binaries, and helm charts information for specified scenario.
    """

    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    build_info_scenario = scenario
    # For "development" builds, we use the "patch" scenario to get the build info
    if scenario == BuildScenario.DEVELOPMENT:
        build_info_scenario = BuildScenario.PATCH

    images = {}
    for name, data in build_info["images"].items():
        scenario_data = data.get(build_info_scenario)
        if not scenario_data:
            # If no scenario_data is available for the scenario, skip this image
            continue

        images[name] = ImageInfo(
            repositories=scenario_data["repositories"],
            platforms=scenario_data["platforms"],
            dockerfile_path=data["dockerfile-path"],
            sign=scenario_data.get("sign", False),
            latest_tag=scenario_data.get("latest-tag", False),
            olm_tag=scenario_data.get("olm-tag", False),
            skip_if_exists=scenario_data.get("skip-if-exists", False),
        )

    binaries = {}
    for name, data in build_info["binaries"].items():
        scenario_data = data.get(build_info_scenario)
        if not scenario_data:
            # If no scenario_data is available for the scenario, skip this binary
            continue

        binaries[name] = BinaryInfo(
            s3_store=scenario_data["s3-store"],
            platforms=scenario_data["platforms"],
            sign=scenario_data.get("sign", False),
        )

    helm_charts = {}
    for name, data in build_info["helm-charts"].items():
        scenario_data = data.get(build_info_scenario)
        if not scenario_data:
            # If no scenario_data is available for the scenario, skip this helm-chart
            continue

        helm_charts[name] = HelmChartInfo(
            repositories=scenario_data["repositories"],
            sign=scenario_data.get("sign", False),
        )

    return BuildInfo(images=images, binaries=binaries, helm_charts=helm_charts)
