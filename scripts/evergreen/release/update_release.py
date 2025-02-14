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

    with open(release, "w") as f:
        json.dump(
            data,
            f,
            indent=2,
        )
        f.write("\n")


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
        "operator",
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


update_release_json()
