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


def set_community_operator_library_version(version: str):
    with open("go.mod") as f:
        go_mod_contents = f.read()

    # update go mod with the new version
    with open("go.mod", "w+") as f:
        subbed = re.sub(
            r"github.com/mongodb/mongodb-kubernetes-operator v.*$",
            f"github.com/mongodb/mongodb-kubernetes-operator v{version}",
            go_mod_contents,
            flags=re.MULTILINE,
        )
        f.write(subbed)

    print("Running 'go mod download'")
    os.system("go mod download")


def main() -> int:
    release = load_release()
    for k in release:
        if k in RELEASE_JSON_TO_HELM_KEY:
            update_all_helm_values_files(RELEASE_JSON_TO_HELM_KEY[k], release[k])

    set_community_operator_library_version(release["communityOperatorVersion"])

    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.opsManager",
        release["supportedOpsManagerVersions"],
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.mongodb",
        release["supportedAppDBVersions"],
    )
    set_value_in_yaml_file(
        "helm_chart/values-openshift.yaml",
        "relatedImages.agent",
        release["supportedAgentVersions"],
    )

    return 0


if __name__ == "__main__":
    sys.exit(main())
