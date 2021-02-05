#!/usr/bin/env python3
#

import os
import sys
from typing import Dict, List

import requests
from git import Repo

from add_supported_release import get_repo_root

SUPPORTED_IMAGES = (
    "database",
    "init-database",
    "appdb",
    "init-appdb",
    "ops-manager",
    "init-ops-manager",
    "operator",
)


URL_LOCATION_BASE = (
    "https://enterprise-operator-dockerfiles.s3.amazonaws.com/dockerfiles"
)

LOCAL_DOCKERFILE_LOCATION = "public/dockerfiles"
DOCKERFILE_NAME = "Dockerfile"


def get_supported_version_for_image(image: str) -> List[Dict[str, str]]:
    supported_versions = (
        "https://webhooks.mongodb-realm.com/api/client/v2.0/app/"
        "kubernetes-release-support-kpvbh/service/"
        "supported-{}-versions/incoming_webhook/list".format(image)
    )

    return requests.get(supported_versions).json()


def download_dockerfile_from_s3(image: str, version: str, distro: str) -> str:
    url = (
        f"{URL_LOCATION_BASE}/mongodb-enterprise-{image}/Dockerfile.{distro}-{version}"
    )
    return requests.get(url).text


def git_add_dockerfiles(base_directory: str):
    """Looks for all of the `Dockerfile`s in the public/dockerfiles
    directory and stages them in git."""
    repo = Repo()
    public_dir = os.path.join(get_repo_root(), LOCAL_DOCKERFILE_LOCATION)

    for root, _, files in os.walk(public_dir):
        for fname in files:
            if fname != DOCKERFILE_NAME:
                continue

            repo.index.add(os.path.join(root, fname))


def save_supported_dockerfiles():
    """
    Finds every supported release in the Atlas database and downloads the corresponding
    Dockerfile.
    """
    for image in SUPPORTED_IMAGES:
        print("Image:", image)
        versions = get_supported_version_for_image(image)
        for version in versions:
            for variant in version["variants"]:
                version_str = version["version"]
                dockerdir = f"{LOCAL_DOCKERFILE_LOCATION}/mongodb-enterprise-{image}/{version_str}/{variant}"
                os.makedirs(dockerdir, exist_ok=True)
                dockerfile = download_dockerfile_from_s3(
                    image, version["version"], variant
                )
                dockerpath = os.path.join(dockerdir, DOCKERFILE_NAME)
                with open(dockerpath, "w") as fd:
                    fd.write(dockerfile)
                    print("* {} - {}: {}".format(version_str, variant, dockerpath))


def main() -> int:
    save_supported_dockerfiles()
    git_add_dockerfiles(LOCAL_DOCKERFILE_LOCATION)

    return 0


if __name__ == "__main__":
    sys.exit(main())
