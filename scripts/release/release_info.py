import argparse
import json
import os

from lib.base_logger import logger
from scripts.release.build.build_info import (
    AGENT_IMAGE,
    DATABASE_IMAGE,
    INIT_APPDB_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    OPERATOR_IMAGE,
    OPS_MANAGER_IMAGE,
    READINESS_PROBE_IMAGE,
    UPGRADE_HOOK_IMAGE,
    BuildInfo,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.build.image_build_process import (
    DockerImageBuilder,
)
from scripts.release.kubectl_mongodb.utils import (
    upload_assets_to_github_release,
)

SEARCH_IMAGE = "search"
SEARCH_IMAGE_REPOSITORY = "quay.io/mongodb/mongodb-search"

AGENT_IMAGE_REPOSITORY = "quay.io/mongodb/mongodb-agent"

MONGODB_ENTERPRISE_SERVER_IMAGE = "mongodb-enterprise-server"
MONGODB_ENTERPRISE_SERVER_REPOSITORY = "quay.io/mongodb/mongodb-enterprise-server"

RELEASE_INFO_IMAGES_ORDERED = [
    OPERATOR_IMAGE,  # mongodb-kubernetes
    INIT_DATABASE_IMAGE,  # mongodb-kubernetes-init-database
    INIT_APPDB_IMAGE,  # mongodb-kubernetes-init-appdb
    INIT_OPS_MANAGER_IMAGE,  # mongodb-kubernetes-init-ops-manager
    DATABASE_IMAGE,  # mongodb-kubernetes-database
]


def create_release_info_json(operator_version: str) -> str:
    build_info = load_build_info(scenario=BuildScenario.RELEASE)

    release_json_path = os.path.join(os.getcwd(), "release.json")

    release_info_json = convert_to_release_info_json(build_info, release_json_path, operator_version)

    return json.dumps(release_info_json, indent=2)


def convert_to_release_info_json(build_info: BuildInfo, release_json_path: str, operator_version: str) -> dict:
    with open(release_json_path, "r") as fd:
        release_data = json.load(fd)

    release_info_output = {
        "images": {},
    }
    # Filter (and order) images to include only those relevant for release info
    images = {name: build_info.images[name] for name in RELEASE_INFO_IMAGES_ORDERED}

    for name, image in images.items():
        add_image_info(release_info_output, name, image.repositories[0], image.platforms, operator_version)

    # add OPS manager image info
    om_build_info = build_info.images[OPS_MANAGER_IMAGE]
    add_image_info(
        release_info_output,
        OPS_MANAGER_IMAGE,
        om_build_info.repositories[0],
        om_build_info.platforms,
        latest_om_version(release_data),
    )

    # add agent image info
    agent_build_info = build_info.images[AGENT_IMAGE]
    add_image_info(
        release_info_output,
        AGENT_IMAGE,
        AGENT_IMAGE_REPOSITORY,
        agent_build_info.platforms,
        latest_agent_version(release_data),
    )

    # add upgrade hook image info
    upgradehook_build_info = build_info.images[UPGRADE_HOOK_IMAGE]
    add_image_info(
        release_info_output,
        UPGRADE_HOOK_IMAGE,
        upgradehook_build_info.repositories[0],
        upgradehook_build_info.platforms,
        latest_upgrade_hook_version(release_data),
    )

    # add readiness image info
    readiness_build_info = build_info.images[READINESS_PROBE_IMAGE]
    add_image_info(
        release_info_output,
        READINESS_PROBE_IMAGE,
        readiness_build_info.repositories[0],
        readiness_build_info.platforms,
        latest_readiness_version(release_data),
    )

    # add search image info
    add_image_info(
        release_info_output,
        SEARCH_IMAGE,
        SEARCH_IMAGE_REPOSITORY,
        ["linux/arm64", "linux/amd64"],
        latest_search_version(release_data),
    )

    add_image_info(
        release_info_output,
        MONGODB_ENTERPRISE_SERVER_IMAGE,
        MONGODB_ENTERPRISE_SERVER_REPOSITORY,
        ["linux/arm64", "linux/amd64"],
        latest_enterprise_server_version(release_data),
    )

    release_info_output = add_om_agent_mappings(release_data, release_info_output)

    return release_info_output


def add_image_info(release_info_output, name, repository: str, platforms, version):
    digest = manifest_list_digest_for_image(f"{repository}:{version}")
    release_info_output["images"][name] = {
        "repoURL": repository,
        "platforms": platforms,
        "tag": version,
        "digest": digest,
    }


def add_om_agent_mappings(release_data, output):
    om_agent_mapping = release_data["latestOpsManagerAgentMapping"]
    output["latestOpsManagerAgentMapping"] = om_agent_mapping

    return output


def latest_enterprise_server_version(release_data):
    return release_data["supportedImages"]["mongodb-enterprise-server"]["versions"][-1]


def latest_readiness_version(release_data):
    return release_data["readinessProbeVersion"]


def latest_upgrade_hook_version(relese_data):
    return relese_data["versionUpgradeHookVersion"]


def latest_om_version(release_data):
    return release_data["supportedImages"]["ops-manager"]["versions"][-1]


def latest_agent_version(release_data):
    newest_om_version = release_data["supportedImages"]["ops-manager"]["versions"][-1]
    newest_om_mapping = release_data["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][
        newest_om_version
    ]
    return newest_om_mapping["agent_version"]


def latest_search_version(release_data):
    return release_data["search"]["version"]


# manifest_list_digest_for_image returns manifest list digest for the passed image. Returns
# empty string if there was an error figuring that out.
def manifest_list_digest_for_image(image: str) -> str:
    builder = DockerImageBuilder()
    try:
        digest = builder.get_manfiest_list_digest(image)
    except Exception as e:
        logger.error(f"There was an error, figuring out manifest list digest for image {image}. Error: {e}")
        return ""

    return digest


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
    try:
        upload_assets_to_github_release([release_info_filename], args.version)
    except Exception as e:
        raise e
