#!/usr/bin/env python3

import json
import logging
import os

import semver
import yaml

logger = logging.getLogger(__name__)


def get_latest_om_versions_from_evergreen_yml():
    # Define a custom constructor to preserve the anchors in the YAML file
    evergreen_file = os.path.join(os.getcwd(), ".evergreen.yml")
    with open(evergreen_file) as f:
        data = yaml.safe_load(f)
    return data["variables"][0], data["variables"][1]


def update_release_json():
    # Define a custom constructor to preserve the anchors in the YAML file
    release = os.path.join(os.getcwd(), "release.json")
    with open(release, "r") as fd:
        data = json.load(fd)

    # PCT already bumps the release.json, such that the last element contains the newest version, since they are sorted
    newest_om_version = data["supportedImages"]["ops-manager"]["versions"][-1]
    update_mongodb_tools_bundle(data, newest_om_version)

    # PCT bumps this field, and we can use this as a base to set the version for everything else in release.json
    newest_operator_version = data["mongodbOperator"]
    update_operator_related_versions(data, newest_operator_version)

    # Adds mapping between latest major version of OM and agent to the release.json
    update_latest_om_agent_mapping(data)

    with open(release, "w") as f:
        json.dump(
            data,
            f,
            indent=2,
        )
        f.write("\n")


def update_latest_om_agent_mapping(data):
    """
    Updates the 'latestOpsManagerAgentMapping' in release.json with
    newly released Ops Manager version and its corresponding Agent version.

    If a OM's major version entry already exists, it updates the 'opsManagerVersion'
    for that entry. Otherwise, it adds a new entry for the major version.

    Args:
        data (dict): The complete configuration dictionary.
    """

    # since we don't really know which OM version was released (OM is not alway release in semver increasing order),
    # we will have to check all the highest version of every major version in supportedImages.ops-manager.versions
    # and for those will have to make sure we have respective entry in latestOpsManagerAgentMapping.
    om_versions = data["supportedImages"]["ops-manager"]["versions"]
    # highest_versions_map is just going to have major version and and it's respective highest full version
    # {6: Version(major=6, minor=0, patch=27, prerelease=None, build=None), 7: Version(major=7, minor=0, patch=20, prerelease=None, build=None), 8: Version(major=8, minor=0, patch=18, prerelease=None, build=None)} 
    highest_versions_map = {}
    for version in om_versions:
        current_ver = semver.Version.parse(version)
        if current_ver.major in highest_versions_map:
            stored_ver = highest_versions_map[current_ver.major]
            if current_ver > stored_ver:
                highest_versions_map[current_ver.major] = current_ver
        else:
            highest_versions_map[current_ver.major] = current_ver

    # final_output iterates over highest_versions_map and creates a list with OM Version and respective agent version
    final_output = []
    for major in sorted(highest_versions_map.keys()):
        version_obj = highest_versions_map[major]
        final_output.append(
            {
                str(major): {
                    "opsManagerVersion": str(version_obj),
                    "agentVersion": data["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][
                        str(version_obj)
                    ]["agent_version"],
                },
            }
        )

    data["latestOpsManagerAgentMapping"] = final_output

def update_operator_related_versions(release: dict, version: str):
    """
    Updates version on `source`, that corresponds to `release.json`.
    """

    logger.debug(f"Updating release.json for version: {version}")

    keys_to_update_with_current_version = [
        "initDatabaseVersion",
        "initOpsManagerVersion",
        "initAppDbVersion",
        "databaseImageVersion",
    ]

    for key in keys_to_update_with_current_version:
        release[key] = version

    keys_to_add_supported_versions = [
        "mongodb-kubernetes",
        "init-ops-manager",
        "init-database",
        "init-appdb",
        "database",
    ]

    for key in keys_to_add_supported_versions:
        if version not in release["supportedImages"][key]["versions"]:
            release["supportedImages"][key]["versions"].append(version)

    logger.debug(f"Updated content {release}")


def update_mongodb_tools_bundle(data, newest_om_version):
    om_mapping = data["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][newest_om_version]
    mongo_tool_version = om_mapping["tools_version"]

    version_name = f"mongodb-database-tools-rhel88-x86_64-{mongo_tool_version}.tgz"
    data["mongodbToolsBundle"]["ubi"] = version_name


if __name__ == "__main__":
    update_release_json()
