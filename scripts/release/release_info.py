import argparse
import json
import pathlib

from scripts.release.build.build_info import (
    DATABASE_IMAGE,
    INIT_APPDB_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    OPERATOR_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
    BuildInfo,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_RELEASE_INITIAL_VERSION,
    DEFAULT_REPOSITORY_PATH,
)

RELEASE_INFO_IMAGES_ORDERED = [
    OPERATOR_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_APPDB_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    DATABASE_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
]

# TODO: this is dummy version, to be replaced with actual versioning logic https://docs.google.com/document/d/1eJ8iKsI0libbpcJakGjxcPfbrTn8lmcZDbQH1UqMR_g/edit?tab=t.45ig7xr3e3w4#bookmark=id.748ik8snxcyl
DUMMY_VERSION = "dummy_version"


def create_release_info_json() -> str:
    build_info = load_build_info(scenario=BuildScenario.RELEASE)

    release_info_json = convert_to_release_info_json(build_info)

    return json.dumps(release_info_json, indent=2)


def convert_to_release_info_json(build_info: BuildInfo) -> dict:
    output = {
        "images": {},
        "binaries": {},
        "helm-charts": {},
    }
    # Filter (and order) images to include only those relevant for release info
    images = {name: build_info.images[name] for name in RELEASE_INFO_IMAGES_ORDERED}

    for name, image in images.items():
        output["images"][name] = {
            "repositories": image.repositories,
            "platforms": image.platforms,
            "version": DUMMY_VERSION,
        }

    for name, binary in build_info.binaries.items():
        output["binaries"][name] = {
            "platforms": binary.platforms,
            "version": DUMMY_VERSION,
        }

    for name, chart in build_info.helm_charts.items():
        output["helm-charts"][name] = {
            "repositories": chart.repositories,
            "version": DUMMY_VERSION,
        }

    return output


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Create relevant release artifacts information in JSON format.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    args = parser.parse_args()

    release_info = create_release_info_json()

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_info)
    else:
        print(release_info)
