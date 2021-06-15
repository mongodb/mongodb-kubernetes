#!/usr/bin/env python

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_release_version.py <version>
"""
import sys
from typing import List
import requests
import re
import os

import semver
from git_diff import path_has_changes
from helm_files_handler import (
    update_all_helm_values_files,
    update_operator_version_chart,
)
from release_json_handler import (
    read_release_from_file,
    update_release_json,
    ReleaseObject,
    read_value_from_file,
)

INIT_DATABASE_PATHS = [
    "docker/mongodb-enterprise-init-database/",
]

INIT_OPS_MANAGER_PATHS = ["docker/mongodb-enterprise-init-ops-manager/"]


def read_version_from_user(release_object: ReleaseObject) -> str:
    current_release = read_release_from_file(release_object)
    new_version = input(
        f"Please enter a new {release_object.value} version [{current_release}]:\n"
    )

    if not new_version:
        new_version = current_release

    if semver.compare(current_release, new_version) > 0:
        raise Exception(
            "New release version ({}) must be greater than or equal to than the current one ({})!".format(
                new_version, current_release
            )
        )
    return new_version


def handle_operator_version():
    new_version = read_version_from_user(ReleaseObject.mongodb_operator)
    update_release_json(ReleaseObject.mongodb_operator, new_version)
    update_all_helm_values_files("operator", new_version)
    update_operator_version_chart(new_version)


def handle_init_image(
    release_object: ReleaseObject,
    paths_to_check: List[str],
    helm_value_key: str,
    current_operator_version: str,
):
    print("\nChecking if {} needs a version bump".format(release_object.value))
    bump_version = False
    for path in paths_to_check:
        if path_has_changes(path, current_operator_version):
            print("=> Path {} has changed".format(path))
            bump_version = True

    if bump_version:
        new_version = read_version_from_user(release_object)
        update_release_json(release_object, new_version)
        update_all_helm_values_files(helm_value_key, new_version)
    else:
        print(
            f"{release_object} scripts have not changed - not increasing the version!"
        )


def bump_community_operator_library_version():
    quay_url = "https://quay.io/api/v1/repository/mongodb/mongodb-kubernetes-operator"
    resp = requests.get(quay_url).json()
    tags = list(resp["tags"].keys())
    # sort the tags based on version
    tags.sort(key=lambda tag: list(map(int, tag.split("."))))
    latest_release = tags[-1]

    print(
        f"Ensuring go.mod contains the latest version of the Community Operator package: [{latest_release}]"
    )

    with open("go.mod", "r") as f:
        go_mod_contents = f.read()

    # update go mod with the new version
    with open("go.mod", "w+") as f:
        subbed = re.sub(
            r"github.com/mongodb/mongodb-kubernetes-operator v.*$",
            f"github.com/mongodb/mongodb-kubernetes-operator v{latest_release}",
            go_mod_contents,
            flags=re.MULTILINE,
        )
        f.write(subbed)

    print("Running 'go mod download'")
    os.system("go mod download")


def main() -> int:
    current_operator_version = read_release_from_file(ReleaseObject.mongodb_operator)
    handle_operator_version()
    handle_init_image(
        ReleaseObject.init_database,
        INIT_DATABASE_PATHS,
        "initDatabase",
        current_operator_version,
    )
    handle_init_image(
        ReleaseObject.init_appdb,
        INIT_DATABASE_PATHS,
        "initAppDb",
        current_operator_version,
    )
    handle_init_image(
        ReleaseObject.init_om,
        INIT_OPS_MANAGER_PATHS,
        "initOpsManager",
        current_operator_version,
    )
    bump_community_operator_library_version()
    return 0


if __name__ == "__main__":
    sys.exit(main())
