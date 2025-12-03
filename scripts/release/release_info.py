import argparse
import json
import os

from scripts.release.build.build_info import (
    DATABASE_IMAGE,
    INIT_APPDB_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    OPERATOR_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
    OPS_MANAGER_IMAGE,
    AGENT_IMAGE,
    BuildInfo,
    load_build_info,
)
from scripts.release.kubectl_mongodb.promote_kubectl_plugin import upload_assets_to_github_release
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_RELEASE_INITIAL_VERSION,
    DEFAULT_REPOSITORY_PATH,
)

SEARCH_IMAGE = "search"
SEARCH_IMAGE_REPOSITORY = "quay.io/mongodb/mongodb-search"

RELEASE_INFO_IMAGES_ORDERED = [
    OPERATOR_IMAGE, # mongodb-kubernetes
    INIT_DATABASE_IMAGE, # mongodb-kubernetes-init-database
    INIT_APPDB_IMAGE, # mongodb-kubernetes-init-appdb
    INIT_OPS_MANAGER_IMAGE, # mongodb-kubernetes-init-ops-manager
    DATABASE_IMAGE, # mongodb-kubernetes-database
    READINESS_PROBE_IMAGE, # mongodb-kubernetes-readinessprobe
    UPGRADE_HOOK_IMAGE, # mongodb-kubernetes-operator-version-upgrade-post-start-hook
]

EXTERNAL_INFO_IMAGES = [
    OPS_MANAGER_IMAGE,
    AGENT_IMAGE
]

def create_release_info_json(version: str) -> str:
    build_info = load_build_info(scenario=BuildScenario.RELEASE)

    release_info_json = convert_to_release_info_json(build_info, version)

    return json.dumps(release_info_json, indent=2)


def convert_to_release_info_json(build_info: BuildInfo, version: str) -> dict:
    release_json_data = os.path.join(os.getcwd(), "release.json")
    with open(release_json_data, "r") as fd:
        release_data = json.load(fd)

    release_info_output = {
        "images": {},
    }
    # Filter (and order) images to include only those relevant for release info
    images = {name: build_info.images[name] for name in RELEASE_INFO_IMAGES_ORDERED + EXTERNAL_INFO_IMAGES}

    for name, image in images.items():
        output["images"][name] = {
            "repository": image.repository,
            "platforms": image.platforms,
        }
        
        if name == OPS_MANAGER_IMAGE:
            release_info_output["images"][name]["version"] = latest_om_version(release_data)
            continue

        if name == AGENT_IMAGE:
            release_info_output["images"][name]["version"] = latest_agent_version(release_data)
            continue

        release_info_output["images"][name]["version"] = version

    # add search image detail
    release_info_output["images"][SEARCH_IMAGE] = {
                "repositories": SEARCH_IMAGE_REPOSITORY,
                "platforms": ["linux/arm64", "linux/amd64"],
                "version": latest_search_version(release_data)
            }

    release_info_output = add_om_agent_mappings(release_data, release_info_output)

    return release_info_output

def add_om_agent_mappings(release_data, output):
    om_agent_mapping = release_data["latestOpsManagerAgentMapping"]
    output["latestOpsManagerAgentMapping"] = om_agent_mapping

    return output

def latest_om_version(release_data):
    return release_data["supportedImages"]["ops-manager"]["versions"][-1]

def latest_agent_version(release_data):
    newest_om_version = release_data["supportedImages"]["ops-manager"]["versions"][-1]
    newest_om_mapping = release_data["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][newest_om_version]
    return newest_om_mapping["agent_version"]

def latest_search_version(release_data):
    return release_data["search"]["version"]

if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Create relevant release artifacts information in JSON format.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument("--version", help="released MCK version", required=True)
    args = parser.parse_args()

    release_info_filename = f"release_info_{args.version}.json"

    release_info = create_release_info_json(args.version)

    if release_info_filename is not None:
        with open(release_info_filename, "w") as file:
            file.write(release_info)

    upload_assets_to_github_release([release_info_filename], args.version)
