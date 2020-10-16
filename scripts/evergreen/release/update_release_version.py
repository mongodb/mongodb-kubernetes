#!/usr/bin/env python

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_release_version.py <version>
"""
import sys
from typing import List

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
)

INIT_DATABASE_PATHS = [
    "docker/mongodb-enterprise-init-database/",
    "probe/readiness.go",
    "probe/readiness_types.go",
]

INIT_OPS_MANAGER_PATHS = ["docker/mongodb-enterprise-init-ops-manager/"]


def read_version_from_user(release_object: ReleaseObject) -> str:
    current_release = read_release_from_file(release_object)
    new_version = input(
        f"Please enter a new {release_object.value} version (current one: {current_release}):\n"
    )
    if semver.compare(current_release, new_version) >= 0:
        raise Exception(
            "New release version ({}) must be bigger than the current one ({})!".format(
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
    content_changed = [
        path_has_changes(path, current_operator_version) for path in paths_to_check
    ]
    if any(content_changed):
        new_version = read_version_from_user(release_object)
        update_release_json(release_object, new_version)
        update_all_helm_values_files(helm_value_key, new_version)
    else:
        print(
            f"{release_object} scripts have not changed - not increasing the version!"
        )


def main():
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

    return 0


if __name__ == "__main__":
    sys.exit(main())
