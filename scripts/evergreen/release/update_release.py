#!/usr/bin/env python3

import json
import logging
import os

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
    update_latest_om_agent_mapping(data, newest_om_version)

    with open(release, "w") as f:
        json.dump(
            data,
            f,
            indent=2,
        )
        f.write("\n")


def update_latest_om_agent_mapping(data, new_om_version):
    """
    Updates the 'latestOpsManagerAgentMapping' in release.json with
    newly released Ops Manager version and its corresponding Agent version.

    If a OM's major version entry already exists, it updates the 'opsManagerVersion'
    for that entry. Otherwise, it adds a new entry for the major version.

    Args:
        data (dict): The complete configuration dictionary.
        new_om_version (str): The new Ops Manager version (e.g., "8.0.11").
    """

    try:
        om_agent_mapping = data["latestOpsManagerAgentMapping"]
    except KeyError:
        logger.error("Error: 'latestOpsManagerAgentMapping' field not found in the release.json data.")

    new_agent_version = data["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"][new_om_version][
        "agent_version"
    ]

    try:
        new_om_major_version = new_om_version.split(".")[0]
    except IndexError:
        logger.error(f"Error: Invalid version format for new_om_version: {new_om_version}")

    new_om_agent_mapping = {"opsManagerVersion": new_om_version, "agentVersion": new_agent_version}

    new_entry = {new_om_major_version: new_om_agent_mapping}

    major_version_found = False
    for mapping in om_agent_mapping:
        if new_om_major_version in mapping:
            # Update the existing entry
            mapping[new_om_major_version] = new_om_agent_mapping
            major_version_found = True
            logger.info(f"Updated existing entry for major version '{new_om_major_version}' to {new_om_version}.")
            break

    # this is new major version of OM, a new entry will be added
    if not major_version_found:
        om_agent_mapping.append(new_entry)
        logger.info(f"Added new entry for major version '{new_om_major_version}' with version {new_om_version}.")


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
