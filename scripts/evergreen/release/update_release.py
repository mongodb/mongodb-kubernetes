#!/usr/bin/env python3

import json
import logging
import os
from collections import defaultdict

import yaml
from packaging import version

logger = logging.getLogger(__name__)


def get_latest_om_versions_from_evergreen_yml():
    # Define a custom constructor to preserve the anchors in the YAML file
    evergreen_file = os.path.join(os.getcwd(), ".evergreen.yml")
    with open(evergreen_file) as f:
        data = yaml.safe_load(f)
    return data["variables"][0], data["variables"][1]


def trim_versions(versions_list, number_of_versions=3, always_keep=None):
    """
    Keep only the latest number_of_versions versions per major version in a versions list,
    plus any versions specified in always_keep.
    Returns a sorted list with trimmed versions.
    """

    # TODO: mck test release
    if always_keep is None:
        always_keep = ["0.1.0"]

    major_version_groups = defaultdict(list)
    for v in versions_list:
        try:
            major_version = v.split(".")[0]
            major_version_groups[major_version].append(v)
        except (IndexError, AttributeError):
            # Keep versions that don't follow the expected format
            continue

    trimmed_versions = []
    # Add versions that should always be kept
    for v in always_keep:
        if v in versions_list and v not in trimmed_versions:
            trimmed_versions.append(v)

    for major_version, versions in major_version_groups.items():
        versions.sort(key=lambda x: version.parse(x), reverse=True)
        latest_versions = versions[:number_of_versions]
        for v in latest_versions:
            if v not in trimmed_versions:
                trimmed_versions.append(v)

    # Sort the final list in ascending order
    trimmed_versions.sort(key=lambda x: version.parse(x))
    return trimmed_versions


def trim_supported_image_versions(release: dict, image_types: list):
    """
    Trim the versions list for specified image types to keep only
    the latest 3 versions per major version.
    """
    for image_type in image_types:
        if image_type in release["supportedImages"]:
            original_versions = release["supportedImages"][image_type]["versions"]
            trimmed_versions = trim_versions(original_versions, 3)

            # TODO: Remove this once we don't need to use OM 7.0.12 in the OM Multicluster DR tests
            # https://jira.mongodb.org/browse/CLOUDP-297377
            if image_type == "ops-manager":
                trimmed_versions.append("7.0.12")
                trimmed_versions.sort(key=lambda x: version.parse(x))

            release["supportedImages"][image_type]["versions"] = trimmed_versions


def trim_ops_manager_mapping(release: dict):
    """
    Keep only the latest 3 versions per major version in opsManagerMapping.ops_manager.
    """
    if (
        "mongodb-agent" in release["supportedImages"]
        and "opsManagerMapping" in release["supportedImages"]["mongodb-agent"]
    ):
        ops_manager_mapping = release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]

        all_versions = ops_manager_mapping.keys()

        trimmed_versions = trim_versions(all_versions, 3)

        trimmed_mapping = {v: ops_manager_mapping[v] for v in trimmed_versions}

        release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"] = trimmed_mapping


def update_release_json():
    # Define a custom constructor to preserve the anchors in the YAML file
    release = os.path.join(os.getcwd(), "release.json")
    with open(release, "r") as fd:
        data = json.load(fd)

    # Trim ops_manager_mapping to keep only the latest 3 versions
    trim_ops_manager_mapping(data)

    # Trim init and operator images to keep only the latest 3 versions per major
    trim_supported_image_versions(
        data, ["operator", "init-ops-manager", "init-database", "init-appdb", "database", "ops-manager"]
    )

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


if __name__ == "__main__":
    update_release_json()
