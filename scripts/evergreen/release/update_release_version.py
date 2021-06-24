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
import argparse
import requests
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
from functools import cmp_to_key


INIT_DATABASE_PATHS = [
    "docker/mongodb-enterprise-init-database/",
]

INIT_OPS_MANAGER_PATHS = ["docker/mongodb-enterprise-init-ops-manager/"]

QUAY_URL_MAP = {
    "initDatabase": "https://quay.io/api/v1/repository/mongodb/mongodb-enterprise-init-database",
    "initAppDb": "https://quay.io/api/v1/repository/mongodb/mongodb-enterprise-init-appdb",
    "initOpsManager": "https://quay.io/api/v1/repository/mongodb/mongodb-enterprise-init-ops-manager",
    "operator": "https://quay.io/api/v1/repository/mongodb/mongodb-enterprise-operator",
}


def _get_all_released_tags(image_type: str) -> List[str]:
    url = QUAY_URL_MAP[image_type]
    resp = requests.get(url).json()
    tags = resp["tags"]
    return list(tags.keys())


def _get_latest_tag(tags: List[str]) -> str:
    return sorted(tags, key=cmp_to_key(semver.compare), reverse=True)[0]


def read_version_from_user(image_key: str, force: bool) -> str:
    tags = _get_all_released_tags(image_key)
    latest = semver.VersionInfo.parse(_get_latest_tag(tags))
    current_release = str(latest.bump_patch())
    new_version = input(
        f"Please enter a new {image_key} version [{current_release}]:\n"
    )

    if not new_version:
        new_version = current_release

    if new_version in tags:
        if force:
            print(
                f"{image_key}:{new_version} already exists, but --force was specified for this image, continuing..."
            )
        else:
            raise ValueError(
                f"New release version ({new_version}) must not be an already released one!"
            )
    return new_version


def handle_operator_version(force: bool):
    new_version = read_version_from_user("operator", force)
    update_release_json(ReleaseObject.mongodb_operator, new_version)
    update_all_helm_values_files("operator", new_version)
    update_operator_version_chart(new_version)


def handle_init_image(
    release_object: ReleaseObject,
    paths_to_check: List[str],
    helm_value_key: str,
    current_operator_version: str,
    force: bool,
):
    print("\nChecking if {} needs a version bump".format(release_object.value))
    bump_version = False
    for path in paths_to_check:
        if path_has_changes(path, current_operator_version):
            print("=> Path {} has changed".format(path))
            bump_version = True

    if bump_version:
        new_version = read_version_from_user(helm_value_key, force)
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
    latest_release = _get_latest_tag(tags)

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
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--force",
        help="""Comma-separated list of images for which the script won't fail if the specified tags already exists.
        Valid options are "operator", "initDatabase", "initAppDb", "initOpsManager" """,
        nargs="+",
    )
    args = parser.parse_args()
    if args.force is None:
        args.force = []

    current_operator_version = _get_latest_tag(_get_all_released_tags("operator"))
    handle_operator_version("operator" in args.force)

    handle_init_image(
        ReleaseObject.init_database,
        INIT_DATABASE_PATHS,
        "initDatabase",
        current_operator_version,
        "initDatabase" in args.force,
    )
    handle_init_image(
        ReleaseObject.init_appdb,
        INIT_DATABASE_PATHS,
        "initAppDb",
        current_operator_version,
        "initAppDb" in args.force,
    )
    handle_init_image(
        ReleaseObject.init_om,
        INIT_OPS_MANAGER_PATHS,
        "initOpsManager",
        current_operator_version,
        "initOpsManager" in args.force,
    )
    bump_community_operator_library_version()
    return 0


if __name__ == "__main__":
    sys.exit(main())
