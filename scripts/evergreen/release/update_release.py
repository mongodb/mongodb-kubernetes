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


def update_release_json():
    # Define a custom constructor to preserve the anchors in the YAML file
    release = os.path.join(os.getcwd(), "release.json")
    with open(release, "r") as fd:
        data = json.load(fd)

    # Trim ops_manager_mapping to keep only the latest 3 versions
    trim_ops_manager_mapping(data)

    # Trim ops-manager versions to keep only the latest 3 versions per major
    trim_ops_manager_versions(data)

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


def trim_ops_manager_mapping(release: dict):
    """
    Keep only the latest 3 versions per major version in opsManagerMapping.ops_manager.
    """
    if (
        "mongodb-agent" in release["supportedImages"]
        and "opsManagerMapping" in release["supportedImages"]["mongodb-agent"]
    ):
        ops_manager_mapping = release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"]

        major_version_groups = defaultdict(list)
        for v in ops_manager_mapping.keys():
            major_version = v.split(".")[0]
            major_version_groups[major_version].append(v)

        trimmed_mapping = {}

        for major_version, versions in major_version_groups.items():
            versions.sort(key=lambda x: version.parse(x), reverse=True)
            latest_versions = versions[:3]

            for v in latest_versions:
                trimmed_mapping[v] = ops_manager_mapping[v]

        trimmed_mapping = dict(sorted(trimmed_mapping.items(), key=lambda x: version.parse(x[0])))

        release["supportedImages"]["mongodb-agent"]["opsManagerMapping"]["ops_manager"] = trimmed_mapping


def trim_ops_manager_versions(release: dict):
    """
    Keep only the latest 3 versions per major version in supportedImages.ops-manager.versions.
    """
    if "ops-manager" in release["supportedImages"] and "versions" in release["supportedImages"]["ops-manager"]:
        versions = release["supportedImages"]["ops-manager"]["versions"]

        major_version_groups = defaultdict(list)
        for v in versions:
            major_version = v.split(".")[0]
            major_version_groups[major_version].append(v)

        trimmed_versions = []

        for major_version, versions in major_version_groups.items():
            versions.sort(key=lambda x: version.parse(x), reverse=True)
            latest_versions = versions[:3]
            trimmed_versions.extend(latest_versions)

        # Sort the final list in ascending order
        trimmed_versions.sort(key=lambda x: version.parse(x))
        release["supportedImages"]["ops-manager"]["versions"] = trimmed_versions


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
