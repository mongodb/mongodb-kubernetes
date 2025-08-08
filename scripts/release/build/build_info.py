import json
from dataclasses import dataclass
from typing import Dict

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import DEFAULT_REPOSITORY_PATH, DEFAULT_CHANGELOG_PATH, RELEASE_INITIAL_VERSION_ENV_VAR, \
    get_initial_version, get_initial_commit_sha

MEKO_TESTS_IMAGE = "meko-tests"
OPERATOR_IMAGE = "operator"
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
    repository: str
    platforms: list[str]
    version: str
    sign: bool


@dataclass
class BinaryInfo:
    s3_store: str
    platforms: list[str]
    version: str
    sign: bool


@dataclass
class HelmChartInfo:
    repository: str
    version: str
    sign: bool


@dataclass
class BuildInfo:
    images: Dict[str, ImageInfo]
    binaries: Dict[str, BinaryInfo]
    helm_charts: Dict[str, HelmChartInfo]


def load_build_info(scenario: BuildScenario,
                    repository_path: str = DEFAULT_REPOSITORY_PATH,
                    changelog_sub_path: str = DEFAULT_CHANGELOG_PATH,
                    initial_commit_sha: str = None,
                    initial_version: str = None) -> BuildInfo:
    f"""
    Load build information based on the specified scenario.

    :param scenario: BuildScenario enum value indicating the build scenario (e.g., PATCH, STAGING, RELEASE).
    :param repository_path: Path to the Git repository. Default is the current directory `{DEFAULT_REPOSITORY_PATH}`.
    :param changelog_sub_path: Path to the changelog directory relative to the repository root. Default is '{DEFAULT_CHANGELOG_PATH}'.
    :param initial_commit_sha: SHA of the initial commit to start from if no previous version tag is found. If not provided, it will be determined based on `{RELEASE_INITIAL_VERSION_ENV_VAR} environment variable.
    :param initial_version: Initial version to use if no previous version tag is found. If not provided, it will be determined based on `{RELEASE_INITIAL_VERSION_ENV_VAR}` environment variable.
    :return: BuildInfo object containing images, binaries, and helm charts information for specified scenario.
    """

    if not initial_commit_sha:
        initial_commit_sha = get_initial_commit_sha()
    if not initial_version:
        initial_version = get_initial_version()

    version = scenario.get_version(repository_path, changelog_sub_path, initial_commit_sha, initial_version)

    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    images = {}
    for name, env_data in build_info["images"].items():
        data = env_data.get(scenario)
        if not data:
            # If no data is available for the scenario, skip this image
            continue

        # Only update the image_version if it is not already set in the build_info.json file
        image_version = data.get("version")
        if not image_version:
            image_version = version

        images[name] = ImageInfo(
            repository=data["repository"],
            platforms=data["platforms"],
            version=image_version,
            sign=data.get("sign", False),
        )

    binaries = {}
    for name, env_data in build_info["binaries"].items():
        data = env_data.get(scenario)
        if not data:
            # If no data is available for the scenario, skip this binary
            continue

        binaries[name] = BinaryInfo(
            s3_store=data["s3-store"],
            platforms=data["platforms"],
            version=version,
            sign=data.get("sign", False),
        )

    helm_charts = {}
    for name, env_data in build_info["helm-charts"].items():
        data = env_data.get(scenario)
        if not data:
            # If no data is available for the scenario, skip this helm-chart
            continue

        helm_charts[name] = HelmChartInfo(
            repository=data["repository"],
            version=version,
            sign=data.get("sign", False),
        )

    return BuildInfo(images=images, binaries=binaries, helm_charts=helm_charts)
