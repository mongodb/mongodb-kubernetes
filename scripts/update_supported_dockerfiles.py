#!/usr/bin/env python3
#

import json
import os
import subprocess
import sys
from typing import Dict, List

import requests
from evergreen.release.agent_matrix import (
    get_supported_version_for_image_matrix_handling,
)
from git import Repo
from requests import Response


def get_repo_root():
    output = subprocess.check_output("git rev-parse --show-toplevel".split())

    return output.decode("utf-8").strip()


SUPPORTED_IMAGES = (
    "mongodb-agent",
    "mongodb-enterprise-database",
    "mongodb-enterprise-init-database",
    "mongodb-enterprise-init-appdb",
    "mongodb-enterprise-ops-manager",
    "mongodb-enterprise-init-ops-manager",
    "mongodb-enterprise-operator",
)

URL_LOCATION_BASE = "https://enterprise-operator-dockerfiles.s3.amazonaws.com/dockerfiles"

LOCAL_DOCKERFILE_LOCATION = "public/dockerfiles"
DOCKERFILE_NAME = "Dockerfile"


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def get_supported_variants_for_image(image: str) -> List[str]:
    splitted_image_name = image.split("mongodb-enterprise-", 1)
    if len(splitted_image_name) == 2:
        image = splitted_image_name[1]

    return get_release()["supportedImages"][image]["variants"]


def get_supported_version_for_image(image: str) -> List[str]:
    splitted_image_name = image.split("mongodb-enterprise-", 1)
    if len(splitted_image_name) == 2:
        image = splitted_image_name[1]

    return get_supported_version_for_image_matrix_handling(image)


def download_dockerfile_from_s3(image: str, version: str, distro: str) -> Response:
    url = f"{URL_LOCATION_BASE}/{image}/{version}/{distro}/Dockerfile"
    return requests.get(url)


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
    Finds every supported release in the release.json and downloads the corresponding
    Dockerfile.
    """
    for image in SUPPORTED_IMAGES:
        print("Image:", image)
        versions = get_supported_version_for_image(image)
        for version in versions:
            for variant in get_supported_variants_for_image(image):
                response = download_dockerfile_from_s3(image, version, variant)
                if response.ok:
                    dockerfile = response.text
                    docker_dir = f"{LOCAL_DOCKERFILE_LOCATION}/{image}/{version}/{variant}"
                    os.makedirs(docker_dir, exist_ok=True)
                    docker_path = os.path.join(docker_dir, DOCKERFILE_NAME)
                    with open(docker_path, "w") as fd:
                        fd.write(dockerfile)
                        print("* {} - {}: {}".format(version, variant, docker_path))
                else:
                    print("* {} - {}: does not exist".format(version, variant))


def main() -> int:
    save_supported_dockerfiles()
    git_add_dockerfiles(LOCAL_DOCKERFILE_LOCATION)

    return 0


if __name__ == "__main__":
    sys.exit(main())
