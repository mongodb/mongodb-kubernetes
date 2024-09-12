#!/usr/bin/env python3

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_helm_values_files.py
"""
import json
import sys
from typing import List

from agent_matrix import get_supported_version_for_image_matrix_handling
from helm_files_handler import set_value_in_yaml_file, update_all_helm_values_files

RELEASE_JSON_TO_HELM_KEY = {
    "mongodbOperator": "operator",
    "initDatabaseVersion": "initDatabase",
    "initOpsManagerVersion": "initOpsManager",
    "initAppDbVersion": "initAppDb",
    "databaseImageVersion": "database",
    "agentVersion": "agent",
}


def load_release():
    with open("release.json", "r") as fd:
        return json.load(fd)


def filterNonReleaseOut(versions: List[str]) -> List[str]:
    """Filters out all Release Candidate versions"""
    return list(filter(lambda x: "-rc" not in x, versions))


def main() -> int:
    release = load_release()
    for k in release:
        if k in RELEASE_JSON_TO_HELM_KEY:
            update_all_helm_values_files(RELEASE_JSON_TO_HELM_KEY[k], release[k])

    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.opsManager",
        filterNonReleaseOut(release["supportedImages"]["ops-manager"]["versions"]),
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.mongodbLegacyAppDb",
        filterNonReleaseOut(release["supportedImages"]["appdb-database"]["versions"]),
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.mongodb",
        filterNonReleaseOut(release["supportedImages"]["mongodb-enterprise-server"]["versions"]),
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.agent",
        filterNonReleaseOut(get_supported_version_for_image_matrix_handling("mongodb-agent")),
    )

    return 0


if __name__ == "__main__":
    sys.exit(main())
