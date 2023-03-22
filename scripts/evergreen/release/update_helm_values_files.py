#!/usr/bin/env python

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_helm_values_files.py
"""
import json
import os
import sys
import re
from typing import Dict

from helm_files_handler import (
    update_all_helm_values_files,
    set_value_in_yaml_file,
)

RELEASE_JSON_TO_HELM_KEY = {
    "mongodbOperator": "operator",
    "initDatabaseVersion": "initDatabase",
    "initOpsManagerVersion": "initOpsManager",
    "initAppDbVersion": "initAppDb",
    "databaseImageVersion": "database",
    "agentVersion": "agent",
}


def load_release() -> Dict[str, str]:
    with open("release.json", "r") as fd:
        return json.load(fd)


def main() -> int:
    release = load_release()
    for k in release:
        if k in RELEASE_JSON_TO_HELM_KEY:
            update_all_helm_values_files(RELEASE_JSON_TO_HELM_KEY[k], release[k])

    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.opsManager",
        release["supportedImages"]["ops-manager"]["versions"],
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.mongodb",
        release["supportedImages"]["appdb-database"]["versions"],
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.agent",
        release["supportedImages"]["mongodb-agent"]["versions"],
    )

    return 0


if __name__ == "__main__":
    sys.exit(main())
