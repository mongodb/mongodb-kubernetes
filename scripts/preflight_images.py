#!/usr/bin/env python3
import argparse
import concurrent
import json
import logging
import os
import platform
import random
import re
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Dict, Tuple

import requests
from evergreen.release.agent_matrix import (
    get_supported_version_for_image,
)

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
logging.basicConfig(level=LOGLEVEL)


def image_config(
    image: str,
    rh_cert_project_id: str,
    name_prefix: str = "mongodb-kubernetes-",
    name_suffix: str = "",
) -> Tuple[str, Dict[str, str]]:
    args = {
        "registry": f"quay.io/mongodb/{name_prefix}{image}{name_suffix}",
        "image": f"mongodb/{name_prefix}{image}{name_suffix}",
        "rh_cert_project_id": rh_cert_project_id,
    }
    return image, args


def official_server_image(
    image: str,
    rh_cert_project_id: str,
) -> Tuple[str, Dict[str, str]]:
    args = {
        "registry": f"quay.io/mongodb/mongodb-enterprise-server",
        "image": f"mongodb/mongodb-enterprise-server",
        "rh_cert_project_id": rh_cert_project_id,
    }
    return image, args


def args_for_image(image: str) -> Dict[str, str]:
    image_configs = [
        image_config(
            image="database",
            name_suffix="",
            rh_cert_project_id="6809ece067e8953c22ff54fc",
        ),
        official_server_image(
            image="mongodb-enterprise-server",  # official server images
            rh_cert_project_id="643daaa56da4ecc48795693a",
        ),
        image_config(
            image="init-appdb",
            rh_cert_project_id="6809ec113193c2e55779b8dc",
            name_suffix="",
        ),
        image_config(
            image="init-database",
            rh_cert_project_id="680a22928e2dc72376f34990",
            name_suffix="",
        ),
        image_config(
            image="init-ops-manager",
            rh_cert_project_id="680a247d67e8953c22000544",
            name_suffix="",
        ),
        image_config(
            image="mongodb-kubernetes",
            rh_cert_project_id="6809ea243193c2e55779b4a5",
            name_prefix="",
            name_suffix="",
        ),
        image_config(
            image="ops-manager",
            rh_cert_project_id="633fcd36c4ee7ff29edff589",
            name_prefix="mongodb-enterprise-",
            name_suffix="-ubi",
        ),
        image_config(
            image="mongodb-agent", rh_cert_project_id="68e37c471f673a855dfe1a99", name_prefix="", name_suffix=""
        ),
    ]
    images = {k: v for k, v in image_configs}
    return images[image]


def get_api_token():
    token = os.environ.get("rh_pyxis", "")
    return token


def get_release() -> Dict[str, str]:
    return json.load(open("release.json"))


def create_auth_file():
    # In theory, we could remove this as our container images reside in public repo
    # However, due to https://github.com/redhat-openshift-ecosystem/openshift-preflight/issues/685
    # we need to supply a non-empty --docker-config
    public_auth = """
    {
        "auths": {
            "quay.io": {
                "auth": ""
            }
        }
    }
    """
    with open("./temp-authfile.json", "w") as file:
        file.write(public_auth)


def run_preflight_check(image: str, version: str, submit: bool = False) -> int:
    arch = "amd64" if platform.machine() == "x86_64" else "arm64"

    with tempfile.TemporaryDirectory() as tmpdir:
        preflight_command = [
            "preflight",
            "check",
            "container",
            f"{args_for_image(image)['registry']}:{version}",
            "--artifacts",
            f"{tmpdir}",
        ]

        if submit:
            preflight_command.extend(
                [
                    "--submit",
                    f"--pyxis-api-token={get_api_token()}",
                    f"--certification-project-id={args_for_image(image)['rh_cert_project_id']}",
                ]
            )
        preflight_command.append("--docker-config=./temp-authfile.json")
        logging.info(f'Running command: {" ".join(preflight_command)}')

        run_preflight_with_retries(preflight_command, version)

        result_file = os.path.join(f"{tmpdir}", arch, "results.json")

        if os.path.exists(result_file):
            with open(result_file, "r") as f:
                result_data = json.load(f).get("results", "")
                failed = result_data.get("failed")
                errors = result_data.get("errors")
                if failed or errors:
                    logging.error(
                        f"Following errors or failures found for image: {args_for_image(image)['registry']}:{version}, failures: {failed}, {errors}"
                    )
                    return 1
                else:
                    logging.info("Preflight check passed")
                    return 0
        else:
            logging.info(
                f"Result file not found, counting as failed for image: {args_for_image(image)['registry']}:{version}"
            )
            return 1


def run_preflight_with_retries(preflight_command, version, max_retries=5):
    for attempt in range(max_retries):
        try:
            subprocess.run(preflight_command, capture_output=True, check=True)
            return
        except subprocess.CalledProcessError as e:
            if attempt + 1 < max_retries:
                delay = (2**attempt) + random.uniform(0, 1)
                logging.error(f"Attempt {attempt + 1} failed for version {version}: {e.stderr}")
                logging.info(f"Retrying in {delay:.2f} seconds...")
                time.sleep(delay)
            else:
                logging.error(f"All {max_retries} attempts failed for version: {version}")
                raise


def fetch_tags(page, image, regex_filter):
    """Fetch a single page of tags from Quay API."""
    url = f"https://quay.io/api/v1/repository/{image}/tag/?page={page}&limit=100"
    response = requests.get(url)

    if response.status_code != 200:
        return []

    tags = response.json().get("tags", [])

    filtered_tags = [tag["name"] for tag in tags if re.match(regex_filter, tag["name"])]

    return filtered_tags


def get_filtered_tags_parallel(image, max_pages=5, regex_filter=""):
    """retrieves all tags in parallel from the quay endpoint. If not done in parallel it takes around 5 minutes."""
    all_tags = set()
    futures = []
    with ThreadPoolExecutor() as executor:
        for page in range(1, max_pages + 1):
            futures.append(executor.submit(fetch_tags, page, image, regex_filter))

        for future in concurrent.futures.as_completed(futures):
            all_tags.update(future.result())

    return list(all_tags)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", help="image to run preflight checks on", type=str, required=True)
    parser.add_argument(
        "--submit",
        help="submit image for certification (true|false)",
        type=str,
        required=True,
    )
    parser.add_argument("--version", help="specific version to check", type=str, default=None)
    args = parser.parse_args()
    submit = args.submit.lower() == "true"
    image_version = os.environ.get("image_version", args.version)
    image_args = args_for_image(args.image)

    # mongodb-enterprise-server are externally provided. We preflight for all of them.
    if args.image == "mongodb-enterprise-server":
        versions = get_filtered_tags_parallel(
            image=image_args["image"], max_pages=10, regex_filter=r"^[0-9]+\.[0-9]+\.[0-9]+-ubi[89]$"
        )
    else:
        # these are the images we own, we preflight all of them as long as we officially support them in release.json
        versions = get_supported_version_for_image(args.image)

    # Attempt to run a pre-flight check on a single version of the image
    if image_version is not None:
        return preflight_single_image(args, image_version, submit, versions)

    # Attempt to run pre-flight checks on all the supported and unpublished versions of the image
    logging.info(f"preflight for image: {image_args['image']}")
    logging.info(f"preflight for versions: {versions}")

    create_auth_file()

    # Note: if running preflight on image tag (not daily tag) we in turn preflight the corresponding sha it is pointing to.
    return_codes_version = preflight_parallel(args, versions, submit)
    logging.info("preflight complete, printing summary")
    found_error = False
    for return_code, version in return_codes_version:
        if return_code != 0:
            found_error = True
            logging.error(f"failed image: {args.image}:{version} with exit code: {return_code}")
        else:
            logging.info(f"succeeded image: {args.image}:{version}")

    if found_error:
        return 1
    return 0


def preflight_parallel(args, versions, submit):
    with ThreadPoolExecutor() as executor:
        futures = []
        return_codes = []

        for version in versions:
            logging.info(f"Running preflight check for image: {args.image}:{version}")
            future = executor.submit(run_preflight_check, args.image, version, submit)
            futures.append(future)

        # Collect results as they complete
        for future in concurrent.futures.as_completed(futures):
            index = futures.index(future)
            version = versions[index]  # Get the version from the original list
            try:
                result = future.result()
                return_codes.append((result, version))
            except Exception as e:
                return_codes.append((1, version))
                logging.error(f"Preflight check failed with exception: {e}")

    return return_codes


def preflight_single_image(args, image_version, submit, versions):
    logging.info("Submitting preflight check for a single image version")
    if image_version not in versions:
        logging.error(
            f"Version {image_version} for image {args.image} is not supported. Supported versions: {versions}"
        )
        return 1
    else:
        create_auth_file()
        return_code = run_preflight_check(args.image, image_version, submit=submit)
        if return_code != 0:
            logging.error(
                f"Running preflight check for image: {args.image}:{image_version} failed with exit code: {return_code}"
            )
        return return_code


if __name__ == "__main__":
    sys.exit(main())
